package repository

import (
	"context"
	"database/sql"
	"errors"

	"github.com/aero-vault/aero-vault/internal/access"
)

func (s *sqlStore) PutDepartment(ctx context.Context, department access.Department) error {
	_, err := s.db.ExecContext(ctx, s.rebind(
		`INSERT INTO departments (id, tenant_id, parent_id, name, created_at, updated_at)
		 VALUES ($1,$2,NULLIF($3,''),$4,$5,$6)
		 ON CONFLICT (id) DO UPDATE SET
		   tenant_id=excluded.tenant_id, parent_id=excluded.parent_id,
		   name=excluded.name, updated_at=excluded.updated_at`),
		department.ID, department.TenantID, department.ParentID, department.Name,
		accessTimeString(department.CreatedAt), accessTimeString(department.UpdatedAt))
	return err
}

func (s *sqlStore) GetDepartment(ctx context.Context, tenant, id string) (access.Department, error) {
	row := s.db.QueryRowContext(ctx, s.rebind(
		`SELECT id, tenant_id, COALESCE(parent_id,''), name, created_at, updated_at
		 FROM departments WHERE tenant_id=$1 AND id=$2`), tenant, id)
	return scanDepartment(row)
}

func scanDepartment(row rowScanner) (access.Department, error) {
	var department access.Department
	var created, updated string
	if err := row.Scan(&department.ID, &department.TenantID, &department.ParentID,
		&department.Name, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return access.Department{}, access.ErrNotFound
		}
		return access.Department{}, err
	}
	department.CreatedAt, department.UpdatedAt = accessTime(created), accessTime(updated)
	return department, nil
}

func (s *sqlStore) ListDepartments(ctx context.Context, tenant string) ([]access.Department, error) {
	rows, err := s.db.QueryContext(ctx, s.rebind(
		`SELECT id, tenant_id, COALESCE(parent_id,''), name, created_at, updated_at
		 FROM departments WHERE tenant_id=$1 ORDER BY parent_id, name, id`), tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]access.Department, 0)
	for rows.Next() {
		department, scanErr := scanDepartment(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, department)
	}
	return out, rows.Err()
}

func (s *sqlStore) DeleteDepartment(ctx context.Context, tenant, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, s.rebind(
		`WITH RECURSIVE department_tree(id) AS (
		   SELECT id FROM departments WHERE tenant_id=$1 AND id=$2
		   UNION ALL
		   SELECT d.id FROM departments d
		   JOIN department_tree tree ON d.parent_id=tree.id
		   WHERE d.tenant_id=$3
		 ) DELETE FROM resource_acls
		 WHERE tenant_id=$4 AND principal_type=$5
		 AND principal_id IN (SELECT id FROM department_tree)`),
		tenant, id, tenant, tenant, string(access.PrincipalTypeDepartment))
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, s.rebind(
		`DELETE FROM departments WHERE tenant_id=$1 AND id=$2`), tenant, id)
	if err := accessDeleteResult(result, err); err != nil {
		return err
	}
	return tx.Commit()
}

func accessDeleteResult(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return access.ErrNotFound
	}
	return nil
}

func (s *sqlStore) PutDepartmentMember(ctx context.Context, member access.DepartmentMember) error {
	_, err := s.db.ExecContext(ctx, s.rebind(
		`INSERT INTO department_members (tenant_id, department_id, subject_id, role, created_at)
		 VALUES ($1,$2,$3,$4,$5)
		 ON CONFLICT (tenant_id, department_id, subject_id) DO UPDATE SET role=excluded.role`),
		member.TenantID, member.DepartmentID, member.SubjectID, member.Role,
		accessTimeString(member.CreatedAt))
	return err
}

func (s *sqlStore) DeleteDepartmentMember(ctx context.Context, tenant, departmentID, subjectID string) error {
	result, err := s.db.ExecContext(ctx, s.rebind(
		`DELETE FROM department_members
		 WHERE tenant_id=$1 AND department_id=$2 AND subject_id=$3`),
		tenant, departmentID, subjectID)
	return accessDeleteResult(result, err)
}

func (s *sqlStore) ListDepartmentMembers(ctx context.Context, tenant, departmentID string) ([]access.DepartmentMember, error) {
	rows, err := s.db.QueryContext(ctx, s.rebind(
		`SELECT tenant_id, department_id, subject_id, role, created_at
		 FROM department_members WHERE tenant_id=$1 AND department_id=$2
		 ORDER BY subject_id`), tenant, departmentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]access.DepartmentMember, 0)
	for rows.Next() {
		var member access.DepartmentMember
		var created string
		if err := rows.Scan(&member.TenantID, &member.DepartmentID, &member.SubjectID,
			&member.Role, &created); err != nil {
			return nil, err
		}
		member.CreatedAt = accessTime(created)
		out = append(out, member)
	}
	return out, rows.Err()
}

func (s *sqlStore) ListSubjectDepartments(ctx context.Context, tenant, subjectID string) ([]string, error) {
	if subjectID == "" {
		return []string{}, nil
	}
	rows, err := s.db.QueryContext(ctx, s.rebind(
		`WITH RECURSIVE subject_departments(id) AS (
		   SELECT department_id FROM department_members
		   WHERE tenant_id=$1 AND subject_id=$2
		   UNION
		   SELECT d.parent_id FROM departments d
		   JOIN subject_departments sd ON d.id=sd.id
		   WHERE d.tenant_id=$3 AND d.parent_id IS NOT NULL
		 ) SELECT id FROM subject_departments ORDER BY id`), tenant, subjectID, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
