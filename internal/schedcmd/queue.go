package schedcmd

import (
	"time"
)

type QueuedCommand struct {
	ID        string    `gorm:"column:id;primaryKey"`
	Kind      string    `gorm:"column:kind;not null"`
	Payload   string    `gorm:"column:payload;not null"`
	Author    string    `gorm:"column:author;not null"`
	UndoOf    *string   `gorm:"column:undo_of"`
	CreatedAt time.Time `gorm:"column:created_at;not null"`
	Cancelled int       `gorm:"column:cancelled;not null;default:0"`
}

func (QueuedCommand) TableName() string { return "ts_command" }

type QueuedResult struct {
	CommandID string    `gorm:"column:command_id;primaryKey"`
	Status    string    `gorm:"column:status;not null"`
	Message   *string   `gorm:"column:message"`
	Detail    *string   `gorm:"column:detail"`
	UpdatedAt time.Time `gorm:"column:updated_at;not null"`
}

func (QueuedResult) TableName() string { return "ts_command_result" }

const QueueSchemaPostgres = `create table if not exists ts_command (
    id text NOT NULL,
    kind text NOT NULL,
    payload text NOT NULL,
    author text NOT NULL,
    undo_of text,
    created_at timestamp NOT NULL,
    cancelled integer NOT NULL DEFAULT 0,
    PRIMARY KEY (id)
);

create table if not exists ts_command_result (
    command_id text NOT NULL,
    status text NOT NULL,
    message text,
    detail text,
    updated_at timestamp NOT NULL,
    PRIMARY KEY (command_id)
);
`
