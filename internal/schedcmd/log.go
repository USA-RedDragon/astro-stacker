package schedcmd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

var ErrNotFound = errors.New("command not found")

type Filter struct {
	Author   string
	Category Category
	Query    string
	Status   []Status
	Limit    int
	Before   time.Time
}

type Log interface {
	Create(ctx context.Context, r *Record) error
	Get(ctx context.Context, id string) (Record, error)
	List(ctx context.Context, f Filter) ([]Record, error)
	Update(ctx context.Context, id string, fn func(*Record) error) (Record, error)
	Waiting(ctx context.Context) ([]Record, error)
}

type GormLog struct {
	DB *gorm.DB
}

func NewGormLog(db *gorm.DB) *GormLog { return &GormLog{DB: db} }

func (l *GormLog) Create(ctx context.Context, r *Record) error {
	return l.DB.WithContext(ctx).Create(r).Error
}

func (l *GormLog) Get(ctx context.Context, id string) (Record, error) {
	var r Record
	err := l.DB.WithContext(ctx).Where("id = ?", id).Take(&r).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return r, ErrNotFound
	}
	return r, err
}

func (l *GormLog) List(ctx context.Context, f Filter) ([]Record, error) {
	q := l.DB.WithContext(ctx).Model(&Record{})
	if f.Author != "" {
		q = q.Where("author = ?", f.Author)
	}
	if f.Category != "" {
		q = q.Where("category = ?", f.Category)
	}
	if len(f.Status) > 0 {
		q = q.Where("status IN ?", f.Status)
	}
	if s := strings.TrimSpace(strings.ToLower(f.Query)); s != "" {
		q = q.Where("search LIKE ?", "%"+escapeLike(s)+"%")
	}
	if !f.Before.IsZero() {
		q = q.Where("created_at < ?", f.Before)
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	var out []Record
	err := q.Order("created_at DESC").Limit(limit).Find(&out).Error
	return out, err
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func (l *GormLog) Update(ctx context.Context, id string, fn func(*Record) error) (Record, error) {
	var out Record
	err := l.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var r Record
		if err := tx.Where("id = ?", id).Take(&r).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		if err := fn(&r); err != nil {
			return err
		}
		if err := tx.Save(&r).Error; err != nil {
			return fmt.Errorf("save command %s: %w", id, err)
		}
		out = r
		return nil
	})
	return out, err
}

func (l *GormLog) Waiting(ctx context.Context) ([]Record, error) {
	var out []Record
	err := l.DB.WithContext(ctx).Where("status IN ?", []Status{StatusQueued, StatusPending}).Order("created_at ASC").Find(&out).Error
	return out, err
}

func searchText(d Description, author string) string {
	parts := make([]string, 0, 2+len(d.Objects))
	parts = append(parts, d.Title, author)
	for _, o := range d.Objects {
		parts = append(parts, o.Name)
	}
	return strings.ToLower(strings.Join(parts, " "))
}
