package models

import (
	"sync"
	"testing"

	"gorm.io/gorm/schema"
)

func TestDatabaseUniqueColumnsUseConstraintSemantics(t *testing.T) {
	tests := []struct {
		model any
		field string
	}{
		{Supplier{}, "Code"},
		{Category{}, "Name"},
		{Product{}, "SKU"},
		{Requisition{}, "Number"},
		{PurchaseOrder{}, "Number"},
	}

	for _, test := range tests {
		parsed, err := schema.Parse(test.model, &sync.Map{}, schema.NamingStrategy{})
		if err != nil {
			t.Fatalf("parse %T schema: %v", test.model, err)
		}
		field := parsed.LookUpField(test.field)
		if field == nil {
			t.Fatalf("%T.%s not found", test.model, test.field)
		}
		if !field.Unique {
			t.Errorf("%T.%s must use a UNIQUE constraint so AutoMigrate preserves an existing PostgreSQL constraint", test.model, test.field)
		}
	}
}
