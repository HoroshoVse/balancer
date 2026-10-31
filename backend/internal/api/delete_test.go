package api

import (
	"fmt"
	"net/http"
	"os"
	"testing"

	"github.com/balancer/backend/internal/models"
)

func TestDeleteLoadBalancer(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) { checkDeleteLoadBalancer(t, newTestEnv(t)) })
	t.Run("postgres", func(t *testing.T) {
		dsn := os.Getenv("BALANCER_TEST_POSTGRES_DSN")
		if dsn == "" {
			t.Skip("set BALANCER_TEST_POSTGRES_DSN to a disposable PostgreSQL database")
		}
		checkDeleteLoadBalancer(t, newTestEnvDSN(t, dsn))
	})
}

func checkDeleteLoadBalancer(t *testing.T, env *testEnv) {
	t.Helper()
	operator := env.tokens[models.RoleOperator]
	id := env.createLB(sampleLB(18100))
	otherID := env.createLB(sampleLB(18101))
	var lb models.LoadBalancer
	if err := env.db.First(&lb, id).Error; err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/load-balancers/delete?id=%d", id)
	if rec := env.do(http.MethodPost, path, operator, nil); rec.Code != http.StatusOK {
		t.Fatalf("delete with foreign keys enabled: %d %s", rec.Code, rec.Body)
	}
	for _, check := range []struct {
		model any
		where string
		id    uint
	}{
		{&models.LoadBalancer{}, "id = ?", id},
		{&models.BackendGroup{}, "id = ?", lb.BackendGroupID},
		{&models.BackendServer{}, "group_id = ?", lb.BackendGroupID},
	} {
		var count int64
		if err := env.db.Unscoped().Model(check.model).Where(check.where, check.id).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("delete left %d rows in %T", count, check.model)
		}
	}
	var other models.LoadBalancer
	if err := env.db.Preload("BackendGroup.Backends").First(&other, otherID).Error; err != nil || len(other.BackendGroup.Backends) != 2 {
		t.Fatalf("unrelated balancer changed: %+v, %v", other, err)
	}
	if rec := env.do(http.MethodDelete, path, operator, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("second delete: %d %s", rec.Code, rec.Body)
	}
	// Deletion must free the listening socket in both the engine and the API.
	env.createLB(sampleLB(18100))
}

func TestDeletePreservesSharedBackendGroup(t *testing.T) {
	for _, softDeleted := range []bool{false, true} {
		t.Run(fmt.Sprintf("soft_deleted=%v", softDeleted), func(t *testing.T) {
			env := newTestEnv(t)
			id := env.createLB(sampleLB(18102))
			var lb models.LoadBalancer
			if err := env.db.First(&lb, id).Error; err != nil {
				t.Fatal(err)
			}
			other := sampleLB(18103)
			other.BackendGroup = models.BackendGroup{}
			other.BackendGroupID = lb.BackendGroupID
			if err := env.db.Create(&other).Error; err != nil {
				t.Fatal(err)
			}
			if softDeleted {
				if err := env.db.Delete(&other).Error; err != nil {
					t.Fatal(err)
				}
			}
			path := fmt.Sprintf("/api/v1/load-balancers/delete?id=%d", id)
			if rec := env.do(http.MethodPost, path, env.tokens[models.RoleOperator], nil); rec.Code != http.StatusOK {
				t.Fatalf("delete with a shared group: %d %s", rec.Code, rec.Body)
			}
			var group models.BackendGroup
			if err := env.db.Preload("Backends").First(&group, lb.BackendGroupID).Error; err != nil || len(group.Backends) != 2 {
				t.Fatalf("shared group was changed: %+v, %v", group, err)
			}
		})
	}
}

func TestDeleteRollsBackOnCleanupFailure(t *testing.T) {
	env := newTestEnv(t)
	id := env.createLB(sampleLB(18104))
	if err := env.db.Exec(`CREATE TRIGGER reject_group_delete BEFORE DELETE ON backend_groups BEGIN SELECT RAISE(ABORT, 'test cleanup failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/load-balancers/delete?id=%d", id)
	if rec := env.do(http.MethodPost, path, env.tokens[models.RoleOperator], nil); rec.Code != http.StatusInternalServerError {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	var lb models.LoadBalancer
	if err := env.db.Preload("BackendGroup.Backends").First(&lb, id).Error; err != nil || len(lb.BackendGroup.Backends) != 2 {
		t.Fatalf("failed delete was not rolled back: %+v, %v", lb, err)
	}
}
