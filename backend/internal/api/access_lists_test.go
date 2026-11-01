package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/balancer/backend/internal/accesslists"
	"github.com/balancer/backend/internal/models"
)

func TestAddressListsAPI(t *testing.T) {
	e := newTestEnv(t)
	path := "/api/v1/access-lists"
	input := accesslists.Input{Name: "Shared", Entries: "203.0.113.42/24\n2001:db8::1"}
	if rec := e.do("POST", path, e.tokens[models.RoleViewer], input); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer can write: %d", rec.Code)
	}
	rec := e.do("POST", path, e.tokens[models.RoleOperator], input)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var list models.AccessList
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list.Entries != "203.0.113.0/24\n2001:db8::1" || list.EntryCount != 2 {
		t.Fatalf("%+v", list)
	}
	if rec := e.do("GET", path, e.tokens[models.RoleViewer], nil); rec.Code != http.StatusOK {
		t.Fatalf("viewer cannot read: %d", rec.Code)
	}
	lb := sampleLB(19993)
	lb.AccessMode, lb.AccessAllowLists = models.AccessOnly, []uint{list.ID}
	id := e.createLB(lb)
	var saved models.LoadBalancer
	e.db.First(&saved, id)
	if len(saved.AccessAllowLists) != 1 || saved.AccessAllowLists[0] != list.ID {
		t.Fatalf("references not persisted: %+v", saved)
	}
	lb.ID = id
	lb.AccessMode, lb.AccessAllowLists, lb.AccessDenyLists = "", []uint{}, []uint{list.ID}
	if rec := e.do("POST", "/api/v1/load-balancers/update", e.tokens[models.RoleOperator], lb); rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body)
	}
	e.db.First(&saved, id)
	if len(saved.AccessAllowLists) != 0 || len(saved.AccessDenyLists) != 1 {
		t.Fatalf("references not updated: %+v", saved)
	}
	lb.AccessDenyLists = []uint{999999}
	if rec := e.do("POST", "/api/v1/load-balancers/update", e.tokens[models.RoleOperator], lb); rec.Code != http.StatusBadRequest {
		t.Fatalf("accepted missing list: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("POST", "/api/v1/access-lists/delete?id="+fmt.Sprint(list.ID), e.tokens[models.RoleOperator], nil); rec.Code != http.StatusConflict {
		t.Fatalf("deleted active list: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("GET", "/api/v1/access-lists/delete?id="+fmt.Sprint(list.ID), e.tokens[models.RoleOperator], nil); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET deletes lists: %d", rec.Code)
	}
	lb.AccessDenyLists = []uint{}
	if rec := e.do("POST", "/api/v1/load-balancers/update", e.tokens[models.RoleOperator], lb); rec.Code != http.StatusOK {
		t.Fatalf("clear: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("POST", "/api/v1/access-lists/delete?id="+fmt.Sprint(list.ID), e.tokens[models.RoleOperator], nil); rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
}
