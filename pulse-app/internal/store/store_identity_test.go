package store

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestMigration040PinsImmutableStoreKindBeforeFutureDDL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "personal.db")
	personal, err := OpenVault(path, StoreKindPersonal, "store_personal_nik")
	if err != nil {
		t.Fatalf("open personal: %v", err)
	}
	if personal.StoreKind() != StoreKindPersonal || personal.StoreID() != "store_personal_nik" {
		t.Fatalf("unexpected identity: kind=%q id=%q", personal.StoreKind(), personal.StoreID())
	}
	personal.Close()

	if _, err := OpenVault(path, StoreKindDesk, "store_desk_nik_zbs"); !errors.Is(err, ErrStoreIdentityMismatch) {
		t.Fatalf("desk reopened personal store: %v", err)
	}
	if _, err := Open(path); !errors.Is(err, ErrStoreIdentityMismatch) {
		t.Fatalf("legacy local open adopted product store: %v", err)
	}
}

func TestMigration040RecordsStoreApplicabilityAndBinaryFloors(t *testing.T) {
	s, err := OpenVault(filepath.Join(t.TempDir(), "desk.db"), StoreKindDesk, "store_desk_dima_zbs")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	var kind, disposition string
	var readerFloor, writerFloor int
	if err := s.DB().QueryRow(`
		SELECT store_kind, disposition, min_reader_version, min_writer_version
		  FROM schema_migration_applicability WHERE version = 40`,
	).Scan(&kind, &disposition, &readerFloor, &writerFloor); err != nil {
		t.Fatalf("read applicability: %v", err)
	}
	if kind != "desk" || disposition != "applied" || readerFloor != 40 || writerFloor != 40 {
		t.Fatalf("unexpected applicability row: %q %q %d %d", kind, disposition, readerFloor, writerFloor)
	}

	if _, err := s.DB().Exec(`UPDATE store_identity SET store_kind = 'personal' WHERE singleton = 1`); err == nil {
		t.Fatal("store kind mutation unexpectedly succeeded")
	}
	if _, err := s.DB().Exec(`UPDATE store_identity SET store_id = 'store_other' WHERE singleton = 1`); err == nil {
		t.Fatal("store ID mutation unexpectedly succeeded")
	}
	if _, err := s.DB().Exec(`UPDATE store_identity SET min_writer_version = 39 WHERE singleton = 1`); err == nil {
		t.Fatal("binary floor rollback unexpectedly succeeded")
	}
}
