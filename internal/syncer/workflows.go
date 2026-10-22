package syncer

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/thedavidweng/immich-amazon-sync/internal/workflow"
)

var _ workflow.Records = (*Store)(nil)

// WorkflowRecord returns the durable ownership state for one account. A zero
// record means workflow management was never attempted.
func (s *Store) WorkflowRecord(ctx context.Context, accountID string) (workflow.Record, error) {
	record := workflow.Record{AccountID: accountID}
	err := s.db.QueryRowContext(ctx, `SELECT workflow_id,contract,state,error_code,desired_hash,attempts
		FROM workflows WHERE account_id=?`, accountID).
		Scan(&record.WorkflowID, &record.Contract, &record.State, &record.Code, &record.DesiredHash, &record.Attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return record, nil
	}
	if err != nil {
		return workflow.Record{}, errors.New("storage_unavailable")
	}
	return record, nil
}

// SaveWorkflowRecord updates only the workflow ledger row. The caller decides
// whether a remote write may have committed; this method never contacts a remote.
func (s *Store) SaveWorkflowRecord(ctx context.Context, record workflow.Record) error {
	if record.AccountID == "" {
		return errors.New("storage_unavailable")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("storage_unavailable")
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, "INSERT INTO accounts(id) VALUES(?) ON CONFLICT DO NOTHING", record.AccountID); err != nil {
		return errors.New("storage_unavailable")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO workflows(account_id,workflow_id,contract,state,error_code,desired_hash,attempts,updated_at)
		VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(account_id) DO UPDATE SET workflow_id=excluded.workflow_id,contract=excluded.contract,
		state=excluded.state,error_code=excluded.error_code,desired_hash=excluded.desired_hash,
		attempts=excluded.attempts,updated_at=excluded.updated_at`,
		record.AccountID, record.WorkflowID, record.Contract, record.State, record.Code,
		record.DesiredHash, record.Attempts, time.Now().UTC().Format(time.RFC3339Nano))
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return errors.New("storage_unavailable")
	}
	return nil
}

// WorkflowRecords returns every persisted workflow row, including accounts that
// were since removed from configuration, for read-only diagnostics.
func (s *Store) WorkflowRecords(ctx context.Context) ([]workflow.Record, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT account_id,workflow_id,contract,state,error_code,desired_hash,attempts
		FROM workflows ORDER BY account_id`)
	if err != nil {
		return nil, errors.New("storage_unavailable")
	}
	var records []workflow.Record
	for rows.Next() {
		var record workflow.Record
		if err = rows.Scan(&record.AccountID, &record.WorkflowID, &record.Contract, &record.State,
			&record.Code, &record.DesiredHash, &record.Attempts); err != nil {
			break
		}
		records = append(records, record)
	}
	if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
		return nil, errors.New("storage_unavailable")
	}
	return records, nil
}
