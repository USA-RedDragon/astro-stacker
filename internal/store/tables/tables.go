// Package tables reports which tables and columns a database has, with the
// error when it can't tell. gorm's Migrator().HasTable and HasColumn return
// false on any error, so a database that is briefly unreachable reads as one
// without the table.
package tables

import (
	"context"
	"slices"

	"gorm.io/gorm"
)

// Name is the table of value: a table name, or a model gorm can parse.
func Name(db *gorm.DB, value any) (string, error) {
	if s, ok := value.(string); ok {
		return s, nil
	}
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(value); err != nil {
		return "", err
	}
	return stmt.Table, nil
}

// Has reports whether db has the table of value.
func Has(ctx context.Context, db *gorm.DB, value any) (bool, error) {
	name, err := Name(db, value)
	if err != nil {
		return false, err
	}
	list, err := db.WithContext(ctx).Migrator().GetTables()
	if err != nil {
		return false, err
	}
	return slices.Contains(list, name), nil
}

// HasColumn reports whether db has the table of value with the column.
func HasColumn(ctx context.Context, db *gorm.DB, value any, column string) (bool, error) {
	ok, err := Has(ctx, db, value)
	if err != nil || !ok {
		return false, err
	}
	cols, err := db.WithContext(ctx).Migrator().ColumnTypes(value)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(cols, func(c gorm.ColumnType) bool { return c.Name() == column }), nil
}
