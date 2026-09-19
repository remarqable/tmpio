package models

import "gorm.io/gorm/clause"

// forUpdate is SELECT ... FOR UPDATE.
var forUpdate = clause.Locking{Strength: "UPDATE"}
