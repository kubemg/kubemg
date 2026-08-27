package api

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

func (f *fakeStore) AppTemplates(_ context.Context) ([]db.AppTemplate, error) {
	out := make([]db.AppTemplate, 0, len(f.appTemplates))
	for _, template := range f.appTemplates {
		out = append(out, *template)
	}
	slices.SortFunc(out, func(a, b db.AppTemplate) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func (f *fakeStore) AppTemplate(_ context.Context, name string) (*db.AppTemplate, error) {
	template, ok := f.appTemplates[name]
	if !ok {
		return nil, db.ErrNotFound
	}
	copied := *template
	return &copied, nil
}

func (f *fakeStore) PutAppTemplate(_ context.Context, template *db.AppTemplate) error {
	if template.ID == 0 {
		template.ID = f.nextID
		f.nextID++
	}
	template.UpdatedAt = time.Now()
	copied := *template
	f.appTemplates[template.Name] = &copied
	return nil
}

func (f *fakeStore) DeleteAppTemplate(_ context.Context, name string) error {
	if _, ok := f.appTemplates[name]; !ok {
		return db.ErrNotFound
	}
	delete(f.appTemplates, name)
	return nil
}
