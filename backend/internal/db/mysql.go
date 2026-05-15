package db

import (
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func Open(dsn string) (*gorm.DB, error) {
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	if err := db.AutoMigrate(&User{}, &Task{}, &Kind{}, &ScheduledTask{}); err != nil {
		return nil, err
	}
	return db, nil
}
