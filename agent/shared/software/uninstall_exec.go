package software

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/rs/zerolog/log"
)

// ExecuteUninstall removes one package from this endpoint and reports the result
// to the server, mirroring ExecuteInstall so the console's task list does not
// need to know which of the two verbs produced a row.
//
// It differs from the install path in one deliberate way. ErrNotInstalled is
// reported as a success with the reason in the output log, because a machine
// that never had the package already satisfies the requirement. A red row there
// would train operators to ignore the failures that matter.
func ExecuteUninstall(ctx context.Context, serverURL, deviceID, deviceSecret string, rawPayload json.RawMessage) error {
	var payload UninstallPayload
	if err := json.Unmarshal(rawPayload, &payload); err != nil {
		return fmt.Errorf("unmarshal payload: %w", err)
	}
	if payload.TaskID == "" {
		return errors.New("invalid uninstall payload: missing task_id")
	}

	log.Info().
		Str("task_id", payload.TaskID).
		Str("package_name", payload.PackageName).
		Str("package_type", payload.PackageType).
		Msg("received software uninstall job")

	report := func(rep ProgressReport) {
		rep.TaskID = payload.TaskID
		_ = ReportProgress(ctx, serverURL, deviceID, deviceSecret, rep)
	}
	_ = ReportProgress(ctx, serverURL, deviceID, deviceSecret, ProgressReport{
		TaskID: payload.TaskID,
		Status: "uninstalling",
	})

	// Same deadline as an install. A hung uninstaller is the same undiagnosable
	// task a hung installer is.
	runCtx, cancel := taskContext(ctx)
	defer cancel()

	exitCode, output, runErr := Uninstall(runCtx, payload)

	if errors.Is(runErr, ErrNotInstalled) {
		log.Info().Str("task_id", payload.TaskID).Str("package_name", payload.PackageName).
			Msg("uninstall target was not present on this endpoint")
		report(ProgressReport{Status: "success", ExitCode: &exitCode, OutputLog: &output})
		return nil
	}

	// runProcess reports a non-zero exit as an error, so the reboot-required
	// codes have to be read off the exit code rather than off the error. Same
	// rule the install path uses, and for the same reason: the package is gone
	// either way, and red-rowing a completed removal because Windows wants a
	// restart to drop a locked file would be wrong.
	switch {
	case runErr == nil:
		report(ProgressReport{Status: "success", ExitCode: &exitCode, OutputLog: &output})
		log.Info().Str("task_id", payload.TaskID).Int("exit_code", exitCode).
			Msg("package uninstalled successfully")
		return nil

	case payload.PackageType == "msi" && isMSISuccessCode(exitCode):
		note := output + fmt.Sprintf("\n[uninstall completed; exit code %d means a reboot is required to finish]", exitCode)
		report(ProgressReport{Status: "success", ExitCode: &exitCode, OutputLog: &note})
		log.Info().Str("task_id", payload.TaskID).Int("exit_code", exitCode).
			Msg("package uninstalled, reboot required to finish")
		return nil
	}

	failMsg := runErr.Error()
	log.Warn().Str("task_id", payload.TaskID).Int("exit_code", exitCode).
		Str("error", failMsg).Msg("uninstall failed")
	report(ProgressReport{
		Status:       "failed",
		ExitCode:     &exitCode,
		OutputLog:    &output,
		ErrorMessage: &failMsg,
	})
	return errors.New(failMsg)
}

// isMSISuccessCode reports the exit codes that mean the installer or
// uninstaller did its job: 0, 1641 (ERROR_SUCCESS_REBOOT_INITIATED) and 3010
// (ERROR_SUCCESS_REBOOT_REQUIRED).
func isMSISuccessCode(code int) bool {
	return code == 0 || code == 1641 || code == 3010
}
