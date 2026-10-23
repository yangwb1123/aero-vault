package repository

import (
	"context"
	"testing"

	"github.com/aero-vault/aero-vault/internal/spaces"
)

func TestSpaceCoverageBindingAppearsBetweenP0AndP1(t *testing.T) {
	for _, kind := range []spaces.CoverageScopeKind{spaces.CoverageTenant, spaces.CoverageGlobal} {
		t.Run(string(kind), func(t *testing.T) {
			s, dsn := spaceTestDB(t)
			writer := spaceTestOpen(t, dsn)
			if s.db.DB == writer.db.DB {
				t.Fatal("coverage observation requires independent connection pools")
			}
			scope, tenant, known, status := coverageSQLTestTenant("alpha"), "alpha", true, "active"
			if kind == spaces.CoverageGlobal {
				scope, tenant, known, status = spaces.CoverageScope{Kind: kind}, "beta", false, ""
			}
			p0 := coverageSQLTestObserve(t, s, coverageSQLTestWant(scope, known, status))
			// D is fully materialized before the other handle's write. Its empty
			// bucket inventory is not authoritative coverage and must not be cached.
			data, err := s.ListBuckets(context.Background(), "alpha")
			spaceTestOK(t, err)
			spaceTestEqual(t, "materialized D is empty", len(data), 0)
			v := coverageSQLTestCreateAfterSignal(t, writer, tenant)
			p1 := coverageSQLTestObserve(t, s, coverageSQLTestWant(scope, known, status, coverageSQLTestWitness(v, "active")))
			if p0.State != spaces.CoverageUnbound || p1.State != spaces.CoverageBound {
				t.Fatalf("P0/D/P1 did not observe committed binding: p0=%+v p1=%+v", p0, p1)
			}
			spaceTestEqual(t, "D remains the original materialized data", len(data), 0)
			spaceTestOK(t, s.Close())
			reopened := spaceTestOpen(t, dsn)
			coverageSQLTestObserve(t, reopened, coverageSQLTestWant(scope, known, status, coverageSQLTestWitness(v, "active")))
		})
	}
}

func coverageSQLTestCreateAfterSignal(t *testing.T, writer *sqlStore, tenant string) spaces.Space {
	t.Helper()
	type outcome struct {
		space spaces.Space
		err   error
	}
	start, done := make(chan struct{}), make(chan outcome, 1)
	command := spaceTestCreate(tenant, spaces.Team, spaceTestSubject("issuer", "human"))
	go func() {
		<-start
		v, err := writer.CreateSpace(context.Background(), command)
		done <- outcome{space: v, err: err}
	}()
	close(start)
	result := <-done // Commit finishes before P1; no sleeps or polling.
	spaceTestOK(t, result.err)
	return result.space
}

func TestSpaceCoverageRevisionAndStatusAreFreshAcrossHandles(t *testing.T) {
	s, dsn := spaceTestDB(t)
	writer := spaceTestOpen(t, dsn)
	if s.db.DB == writer.db.DB {
		t.Fatal("not independent pools")
	}
	v := spaceTestSpace(t, writer, spaceTestCreate("alpha", spaces.Team, spaceTestSubject("issuer", "human")))
	tenant, global := coverageSQLTestTenant("alpha"), spaces.CoverageScope{Kind: spaces.CoverageGlobal}
	coverageSQLTestObserve(t, s, coverageSQLTestWant(tenant, true, "active", coverageSQLTestWitness(v, "active")))
	coverageSQLTestObserve(t, s, coverageSQLTestWant(global, false, "", coverageSQLTestWitness(v, "active")))
	// Mutations use the independent handle and real command transaction.
	spaceTestApply(t, writer, &v, spaceTestChange(v, spaceTestSubject("issuer", "viewer"), spaces.AddMember, spaces.Viewer))
	spaceTestOK(t, writer.UpsertTenant(context.Background(), TenantRecord{TenantID: "alpha", Status: "disabled"}))
	coverageSQLTestObserve(t, s, coverageSQLTestWant(tenant, true, "disabled", coverageSQLTestWitness(v, "disabled")))
	coverageSQLTestObserve(t, s, coverageSQLTestWant(global, false, "", coverageSQLTestWitness(v, "disabled")))
}

func TestSpaceCoverageUnknownTenantBecomesKnownBetweenProbes(t *testing.T) {
	s, dsn := spaceTestDB(t)
	writer := spaceTestOpen(t, dsn)
	if s.db.DB == writer.db.DB {
		t.Fatal("not independent pools")
	}
	scope := coverageSQLTestTenant("new-tenant")
	coverageSQLTestObserve(t, s, coverageSQLTestWant(scope, false, ""))
	spaceTestOK(t, writer.UpsertTenant(context.Background(), TenantRecord{TenantID: scope.TenantID, Status: "disabled"}))
	coverageSQLTestObserve(t, s, coverageSQLTestWant(scope, true, "disabled"))
}
