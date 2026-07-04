package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// Procedure is a parameterized, reusable workflow ("skill"/reflex) — the
// procedural memory type, a fourth kind alongside episodic (memory_capsules),
// semantic (assertions), and emotional signal (v3). The engine treats
// params/steps as opaque JSON: it never executes or interprets them, never
// embeds them, and never calls an LLM over them. Nothing in retrieval or the
// write path reads or writes procedures yet — the table is inert until a
// later PR (or an explicit caller) populates it.
type Procedure struct {
	ID           int64
	Name         string // human-readable name
	NameKey      string // derived from Name when empty
	Description  string
	Params       map[string]any // serialized to params_json
	Steps        []any          // serialized to steps_json
	SuccessCount int64
	Scope        Scope // reuse existing store.Scope (Type + ID used here)
	CreatedAt    string
	UpdatedAt    string
}

// MakeProcedureKey normalizes a procedure name into the stable key that two
// re-learnings of the same procedure share, so a later definition can
// supersede an earlier one via upsert. Same normalization shape as the
// assertion claim_key: Cyrillic-aware and punctuation-insensitive.
func MakeProcedureKey(name string) string {
	return normalizeProcedureName(name)
}

func normalizeProcedureName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r >= 'а' && r <= 'я':
			b.WriteRune(r)
		case r == 'ё':
			b.WriteRune('е')
		}
	}
	return b.String()
}

func withDerivedProcedureKey(p Procedure) Procedure {
	if strings.TrimSpace(p.NameKey) == "" {
		p.NameKey = MakeProcedureKey(p.Name)
	}
	return p
}

// procedureScope returns the (type, id) pair used for procedure identity and
// scoping. Procedures do not carry a visibility column, so only Type and ID
// participate; the rest of Scope.normalized() fills the safe defaults.
func procedureScope(sc Scope) (scopeType, scopeID string) {
	n := sc.normalized()
	return n.Type, n.ID
}

func marshalParams(params map[string]any) (string, error) {
	if len(params) == 0 {
		return "{}", nil
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return "", fmt.Errorf("marshal params: %w", err)
	}
	return string(raw), nil
}

func marshalSteps(steps []any) (string, error) {
	if len(steps) == 0 {
		return "[]", nil
	}
	raw, err := json.Marshal(steps)
	if err != nil {
		return "", fmt.Errorf("marshal steps: %w", err)
	}
	return string(raw), nil
}

// UpsertProcedure inserts a new procedure or replaces the existing one with
// the same (name_key, scope). success_count is preserved across an upsert
// unless the caller supplies a higher value. Returns the row id.
func (s *Store) UpsertProcedure(p Procedure) (int64, error) {
	p = withDerivedProcedureKey(p)
	if strings.TrimSpace(p.NameKey) == "" {
		return 0, fmt.Errorf("procedure needs a name to form a name_key")
	}
	if strings.TrimSpace(p.Name) == "" {
		return 0, fmt.Errorf("procedure.name is required")
	}
	scopeType, scopeID := procedureScope(p.Scope)

	paramsJSON, err := marshalParams(p.Params)
	if err != nil {
		return 0, err
	}
	stepsJSON, err := marshalSteps(p.Steps)
	if err != nil {
		return 0, err
	}

	now := strings.TrimSpace(p.UpdatedAt)
	if now == "" {
		now = nowRFC3339()
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var existingID int64
	var existingCount int64
	err = tx.QueryRow(`
		SELECT id, success_count
		  FROM procedures
		 WHERE name_key = ? AND scope_type = ? AND scope_id = ?`,
		p.NameKey, scopeType, scopeID).Scan(&existingID, &existingCount)
	switch {
	case err == sql.ErrNoRows:
		created := strings.TrimSpace(p.CreatedAt)
		if created == "" {
			created = now
		}
		res, ierr := tx.Exec(`
			INSERT INTO procedures
			  (name, name_key, description, params_json, steps_json,
			   success_count, scope_type, scope_id, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			p.Name, p.NameKey, p.Description, paramsJSON, stepsJSON,
			p.SuccessCount, scopeType, scopeID, created, now)
		if ierr != nil {
			return 0, fmt.Errorf("insert procedure: %w", ierr)
		}
		id, ierr := res.LastInsertId()
		if ierr != nil {
			return 0, ierr
		}
		if cerr := tx.Commit(); cerr != nil {
			return 0, cerr
		}
		return id, nil
	case err != nil:
		return 0, fmt.Errorf("lookup current procedure: %w", err)
	}

	// Existing row: replace definition in place, preserving success_count
	// unless the caller supplies a higher value. created_at is left intact.
	count := existingCount
	if p.SuccessCount > count {
		count = p.SuccessCount
	}
	if _, uerr := tx.Exec(`
		UPDATE procedures
		   SET name = ?, description = ?, params_json = ?, steps_json = ?,
		       success_count = ?, updated_at = ?
		 WHERE id = ?`,
		p.Name, p.Description, paramsJSON, stepsJSON, count, now, existingID); uerr != nil {
		return 0, fmt.Errorf("update procedure: %w", uerr)
	}
	if cerr := tx.Commit(); cerr != nil {
		return 0, cerr
	}
	return existingID, nil
}

// GetProcedure returns the procedure for a normalized name within a scope, or
// (nil, nil) if none.
func (s *Store) GetProcedure(name string, scope Scope) (*Procedure, error) {
	nameKey := MakeProcedureKey(name)
	scopeType, scopeID := procedureScope(scope)
	row := s.db.QueryRow(`
		SELECT id, name, name_key, description, params_json, steps_json,
		       success_count, scope_type, scope_id, created_at, updated_at
		  FROM procedures
		 WHERE name_key = ? AND scope_type = ? AND scope_id = ?`,
		nameKey, scopeType, scopeID)
	p, err := scanProcedure(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

// ListProcedures returns procedures in a scope, most-recently-updated first.
func (s *Store) ListProcedures(scope Scope) ([]Procedure, error) {
	scopeType, scopeID := procedureScope(scope)
	rows, err := s.db.Query(`
		SELECT id, name, name_key, description, params_json, steps_json,
		       success_count, scope_type, scope_id, created_at, updated_at
		  FROM procedures
		 WHERE scope_type = ? AND scope_id = ?
		 ORDER BY updated_at DESC, id DESC`, scopeType, scopeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Procedure
	for rows.Next() {
		p, err := scanProcedureRow(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// IncrementProcedureSuccess bumps success_count by 1 and touches updated_at.
func (s *Store) IncrementProcedureSuccess(id int64, now string) error {
	if strings.TrimSpace(now) == "" {
		now = nowRFC3339()
	}
	_, err := s.db.Exec(`
		UPDATE procedures
		   SET success_count = success_count + 1, updated_at = ?
		 WHERE id = ?`, now, id)
	return err
}

type scanFunc func(dest ...any) error

func scanProcedure(row *sql.Row) (*Procedure, error) {
	return scanProcedureRow(row.Scan)
}

func scanProcedureRow(scan scanFunc) (*Procedure, error) {
	var p Procedure
	var paramsJSON, stepsJSON string
	if err := scan(
		&p.ID, &p.Name, &p.NameKey, &p.Description, &paramsJSON, &stepsJSON,
		&p.SuccessCount, &p.Scope.Type, &p.Scope.ID, &p.CreatedAt, &p.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if strings.TrimSpace(paramsJSON) != "" {
		_ = json.Unmarshal([]byte(paramsJSON), &p.Params)
	}
	if strings.TrimSpace(stepsJSON) != "" {
		_ = json.Unmarshal([]byte(stepsJSON), &p.Steps)
	}
	return &p, nil
}
