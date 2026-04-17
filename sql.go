package main

import (
	"database/sql"
	"fmt"
	"sync"
)

type SQL struct {
	conn     *sql.DB
	objectID string
}

var (
	sqlInstances  = make(map[string]*SQL)
	sqlThreadLock = &sync.RWMutex{}
	instanceLocks = make(map[string]*sync.RWMutex)
)

func NewSQL(objectID string) *SQL {
	sqlThreadLock.Lock()
	defer sqlThreadLock.Unlock()

	if _, exists := sqlInstances[objectID]; !exists {
		sqlInstances[objectID] = &SQL{objectID: objectID}
		instanceLocks[objectID] = &sync.RWMutex{}
	}
	return sqlInstances[objectID]
}

func (sql *SQL) Open(dbOpen func() (*sql.DB, error)) error {
	instanceLock := instanceLocks[sql.objectID]
	instanceLock.Lock()
	defer instanceLock.Unlock()

	if sql.conn == nil {
		var err error
		sql.conn, err = dbOpen()
		if err != nil {
			sql.conn = nil
			return err
		}
	}
	return nil
}

func (sql *SQL) Execute(cmd string, args ...interface{}) ([]map[string]interface{}, error) {
	instanceLock := instanceLocks[sql.objectID]
	instanceLock.Lock()
	defer instanceLock.Unlock()

	if sql.conn == nil {
		return nil, fmt.Errorf("database not open")
	}

	rows, err := sql.conn.Query(cmd, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	count := len(columns)
	var result []map[string]interface{}
	values := make([]interface{}, count)
	valuePtrs := make([]interface{}, count)
	for i := range columns {
		valuePtrs[i] = &values[i]
	}

	for rows.Next() {
		if err := rows.Scan(valuePtrs...); err != nil {
			return nil, err
		}

		entry := make(map[string]interface{})
		for i, col := range columns {
			entry[col] = values[i]
		}
		result = append(result, entry)
	}
	return result, nil
}

func (sql *SQL) Close() {
	instanceLock := instanceLocks[sql.objectID]
	instanceLock.Lock()
	defer instanceLock.Unlock()

	if sql.conn != nil {
		sql.conn.Close()
		sql.conn = nil
	}
}
