package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
	usermgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/user-management"
)

func TestUserManagement_CRUD(t *testing.T) {
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	repo := usermgmt.NewRepository(database)
	ctx := context.Background()

	// 1. Create User
	now := time.Now().UTC()
	hash, err := auth.HashPassword("securepass123")
	if err != nil {
		t.Fatal(err)
	}

	displayName := "John Doe"
	user := &usermgmt.User{
		ID:           usermgmt.NewID(),
		Username:     "johndoe",
		PasswordHash: hash,
		Role:         rbac.RoleTechnician,
		DisplayName:  &displayName,
		IsActive:     true,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	if err := repo.Create(ctx, user); err != nil {
		t.Fatal("create user:", err)
	}
	t.Logf("Created user: %s (ID: %s)", user.Username, user.ID)

	// 2. Prevent Duplicate Username
	dupUser := &usermgmt.User{
		ID:           usermgmt.NewID(),
		Username:     "johndoe",
		PasswordHash: hash,
		Role:         rbac.RoleViewer,
		IsActive:     true,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := repo.Create(ctx, dupUser); err == nil {
		t.Fatal("expected duplicate username error, got nil")
	}
	t.Log("Duplicate username prevented correctly")

	// 3. Get User By ID
	fetched, err := repo.GetByID(ctx, user.ID)
	if err != nil {
		t.Fatal("get user by id:", err)
	}
	if fetched.Username != "johndoe" || fetched.Role != rbac.RoleTechnician {
		t.Fatalf("unexpected user data: %+v", fetched)
	}

	// 4. Update User Role & Display Name
	newRole := rbac.RoleAdmin
	newDN := "John Admin"
	if err := repo.Update(ctx, user.ID, usermgmt.UpdateUserRequest{
		Role:        &newRole,
		DisplayName: &newDN,
	}); err != nil {
		t.Fatal("update user:", err)
	}

	updated, err := repo.GetByID(ctx, user.ID)
	if err != nil {
		t.Fatal("get updated user:", err)
	}
	if updated.Role != rbac.RoleAdmin || *updated.DisplayName != "John Admin" {
		t.Fatalf("unexpected updated user: %+v", updated)
	}
	t.Log("User updated successfully to role Admin")

	// 5. Update Password
	newHash, _ := auth.HashPassword("newpassword456")
	if err := repo.UpdatePassword(ctx, user.ID, newHash); err != nil {
		t.Fatal("update password:", err)
	}

	passUser, _ := repo.GetByID(ctx, user.ID)
	if err := auth.ComparePassword(passUser.PasswordHash, "newpassword456"); err != nil {
		t.Fatal("new password does not match hash:", err)
	}
	t.Log("Password updated and verified")

	// 6. Deactivate User. johndoe is now the only administrator, and turning
	// off the last active admin is refused; with a second admin it is allowed.
	if err := repo.Deactivate(ctx, user.ID); !errors.Is(err, usermgmt.ErrLastAdmin) {
		t.Fatalf("deactivating the only admin: err = %v, want ErrLastAdmin", err)
	}
	other := &usermgmt.User{
		ID: usermgmt.NewID(), Username: "root", PasswordHash: hash, Role: rbac.RoleAdmin,
		IsActive: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := repo.Create(ctx, other); err != nil {
		t.Fatal("create second admin:", err)
	}
	if err := repo.Deactivate(ctx, user.ID); err != nil {
		t.Fatal("deactivate user:", err)
	}

	deactivated, _ := repo.GetByID(ctx, user.ID)
	if deactivated.IsActive {
		t.Fatal("expected user to be inactive")
	}
	t.Log("User deactivated successfully")

	// 7. List Users
	users, err := repo.List(ctx, 100)
	if err != nil {
		t.Fatal("list users:", err)
	}
	if len(users) != 2 { // johndoe and the second admin
		t.Fatalf("expected 2 users, got %d", len(users))
	}
}
