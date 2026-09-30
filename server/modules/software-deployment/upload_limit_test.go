package softwaredeployment

// The package upload route and the four anonymous JSON routes are capped
// separately, on purpose: the upload legitimately carries hundreds of megabytes
// and a router-wide 1 MiB cap would break it. These tests hold both halves of
// that decision in place -- the upload refuses past its own ceiling, and a body
// under the ceiling still goes through.

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jmoiron/sqlx"

	devicemgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/device-management"
)

// uploadPackageFixture is the set of tables CreatePackage writes to. The upload
// path reaches CreatePackage only after the body has been parsed, so a body that
// the cap refuses never touches these -- they exist so a body that gets through
// can be shown to still work.
func newUploadRouter(t *testing.T) *chi.Mux {
	t.Helper()
	d, err := sqlx.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })

	d.MustExec(`CREATE TABLE software_packages (
		id TEXT PRIMARY KEY, name TEXT, version TEXT, os_target TEXT,
		package_type TEXT, file_name TEXT, file_size INTEGER, sha256 TEXT,
		storage_path TEXT, install_args TEXT, uninstall_args TEXT,
		created_at TEXT, updated_at TEXT);`)

	h := NewHandler(&Repository{db: d}, silentHub{}, silentAuditor{}, t.TempDir(),
		func(n http.Handler) http.Handler { return n },
		lookup{devices: map[string]devicemgmt.Device{}})

	r := chi.NewRouter()
	r.Post("/api/software/packages", h.uploadPackage)
	return r
}

// multipartBody builds a real multipart/form-data body: the form fields the
// upload route reads, plus a file part of `fileSize` bytes.
//
// It streams rather than buffering, so a 2 MB file costs 2 MB of test memory
// instead of the double that a bytes.Buffer would need.
func multipartBody(fileName, pkgType string, fileSize int) (io.Reader, string) {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)

	go func() {
		fields := [][2]string{
			{"name", "testpkg"},
			{"version", "1.0.0"},
			{"os_target", OSTargetWindows},
			{"package_type", pkgType},
			{"install_args", "/quiet"},
		}
		for _, f := range fields {
			_ = mw.WriteField(f[0], f[1])
		}
		part, err := mw.CreateFormFile("file", fileName)
		if err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		// 32 KiB at a time: big enough to be few writes, small enough not to
		// allocate a copy of the whole payload.
		chunk := bytes.Repeat([]byte("M"), 32<<10)
		for written := 0; written < fileSize; written += len(chunk) {
			n := len(chunk)
			if remaining := fileSize - written; remaining < n {
				n = remaining
			}
			if _, err := part.Write(chunk[:n]); err != nil {
				_ = pw.CloseWithError(err)
				return
			}
		}
		_ = mw.Close()
		_ = pw.Close()
	}()

	return pr, mw.FormDataContentType()
}

// TestAnOversizedPackageUploadIsRefused is the regression test for the cap that
// was not a cap.
//
// uploadPackage passed 500 MB to ParseMultipartForm, which takes a memory
// threshold and not a size limit: it means "spill to a temp file past 500 MB",
// and a body of any size is accepted. Nothing rejected a package, and a 2 GB
// body was buffered to disk before any check ran.
func TestAnOversizedPackageUploadIsRefused(t *testing.T) {
	setMaxPackageBytes(t, 1<<20) // 1 MB

	r := newUploadRouter(t)

	const fileSize = 8 << 20 // 8 MB, well past the lowered ceiling
	body, ctype := multipartBody("setup.msi", PkgTypeMSI, fileSize)

	req := httptest.NewRequest(http.MethodPost, "/api/software/packages", body)
	req.Header.Set("Content-Type", ctype)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want %d\nbody: %s",
			rec.Code, http.StatusRequestEntityTooLarge, rec.Body.String())
	}
	// The message has to carry the number, or the operator has no idea what to
	// split the package into.
	if msg := rec.Body.String(); !strings.Contains(msg, "MB limit") {
		t.Errorf("413 body does not state the limit: %s", msg)
	}
}

// TestAPackageUnderTheLimitStillUploads is the other half, and the one that
// matters more if it ever breaks: this route carries real software, and a cap
// that rejected ordinary packages would stop deployments outright.
func TestAPackageUnderTheLimitStillUploads(t *testing.T) {
	setMaxPackageBytes(t, 8<<20) // 8 MB

	r := newUploadRouter(t)

	body, ctype := multipartBody("setup.msi", PkgTypeMSI, 1<<20) // 1 MB
	req := httptest.NewRequest(http.MethodPost, "/api/software/packages", body)
	req.Header.Set("Content-Type", ctype)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201\nbody: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"id"`) {
		t.Errorf("response is not a package: %s", rec.Body.String())
	}
}

// TestTheFormFieldsStillFitUnderTheCap: the ceiling is applied to the whole
// body, file plus fields, with 1 MB of slack. A ceiling applied to the file
// alone would need this to be zero; one applied with no slack would reject a
// file exactly at the limit for the sake of the four fields beside it. Neither
// is wrong, but the reason the slack exists is worth pinning.
func TestTheFormFieldsStillFitUnderTheCap(t *testing.T) {
	const ceiling = 4 << 20
	setMaxPackageBytes(t, ceiling)

	r := newUploadRouter(t)

	// A file exactly at the ceiling, plus form fields. Without the 1 MB of slack
	// this would be refused for being exactly at the limit.
	body, ctype := multipartBody("setup.msi", PkgTypeMSI, ceiling)
	req := httptest.NewRequest(http.MethodPost, "/api/software/packages", body)
	req.Header.Set("Content-Type", ctype)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Errorf("a file exactly at the %d byte ceiling was refused with %d\nbody: %s",
			ceiling, rec.Code, rec.Body.String())
	}
}

// setMaxPackageBytes lowers the ceiling for the duration of one test.
//
// It restores through t.Cleanup rather than a returned func, so a t.Fatal in the
// middle of the test still puts the package back and the next test does not
// inherit a 1 MB ceiling.
func setMaxPackageBytes(t *testing.T, v int64) {
	t.Helper()
	prev := maxPackageBytes
	maxPackageBytes = v
	t.Cleanup(func() { maxPackageBytes = prev })
}
