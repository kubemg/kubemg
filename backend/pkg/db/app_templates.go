package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/kubemg/kubemg/backend/pkg/apptemplate"
)

// appTemplatesSeeded marks that the starter catalogue has been written. The
// marker, rather than "insert if the table is empty", is what makes a
// deliberate deletion stick — the same reasoning as helmRepositoriesSeeded.
const appTemplatesSeeded = "app_templates_seeded"

// SeedAppTemplates writes the starter catalogue once.
func SeedAppTemplates(gdb *gorm.DB) error {
	var marked int64
	if err := gdb.Model(&Setting{}).
		Where("key = ?", appTemplatesSeeded).
		Count(&marked).Error; err != nil {
		return fmt.Errorf("read app template seed marker: %w", err)
	}
	if marked > 0 {
		return nil
	}

	for _, template := range SeededAppTemplates {
		row := template
		row.Seeded = true
		// A name an operator has already taken is left exactly as it is: this
		// is a seed, and overwriting a row somebody wrote would be the seed
		// editing an administrator's work.
		if err := gdb.Clauses(clause.OnConflict{DoNothing: true}).
			Create(&row).Error; err != nil {
			return fmt.Errorf("seed app template %q: %w", template.Name, err)
		}
	}

	if err := gdb.Save(&Setting{Key: appTemplatesSeeded, Value: "1"}).Error; err != nil {
		return fmt.Errorf("mark app template seed: %w", err)
	}
	return nil
}

/* -------------------------------------------------------------- store --- */

// AppTemplates lists every declared template, by name.
func (s *Store) AppTemplates(ctx context.Context) ([]AppTemplate, error) {
	var templates []AppTemplate
	if err := s.gdb.WithContext(ctx).Order("name asc").Find(&templates).Error; err != nil {
		return nil, err
	}
	return templates, nil
}

// AppTemplate reads one by name. The name is the address, exactly as it is for
// a chart repository.
func (s *Store) AppTemplate(ctx context.Context, name string) (*AppTemplate, error) {
	var template AppTemplate
	err := s.gdb.WithContext(ctx).Where("name = ?", strings.TrimSpace(name)).First(&template).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &template, nil
}

// PutAppTemplate writes a template, creating or replacing by name. Seeded is
// deliberately never in the update set — a save can never turn an
// administrator's edit of a starter template into one this build wrote, nor
// the reverse.
func (s *Store) PutAppTemplate(ctx context.Context, template *AppTemplate) error {
	return s.gdb.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "name"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"title", "description", "manifests", "parameters", "created_by", "updated_at",
		}),
	}).Create(template).Error
}

// DeleteAppTemplate removes a template. A seeded row deletes like any other —
// the whole point of seeding rows rather than hard-coding a list.
func (s *Store) DeleteAppTemplate(ctx context.Context, name string) error {
	result := s.gdb.WithContext(ctx).Where("name = ?", strings.TrimSpace(name)).Delete(&AppTemplate{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// mustParams encodes a parameter declaration for a seeded row. It panics on a
// marshal failure because the only way one can fail is a bug in this file —
// the seeds are Go literals, not user input.
func mustParams(params []apptemplate.Parameter) string {
	encoded, err := json.Marshal(params)
	if err != nil {
		panic(fmt.Sprintf("app template seed: %v", err))
	}
	return string(encoded)
}

// SeededAppTemplates is the catalogue a fresh installation starts with — the
// smallest set that makes the feature usable before an administrator has
// written a template of their own, the same argument SeededHelmRepositories
// makes. Every bundle here has to pass apptemplate.ValidateBundle; see
// app_templates_test.go.
var SeededAppTemplates = []AppTemplate{
	{
		Name:        "web-service",
		Title:       "Web service",
		Description: "A Deployment, a Service, and a Gateway API HTTPRoute in front of it.",
		Manifests: `apiVersion: apps/v1
kind: Deployment
metadata:
  name: ${name}
  labels:
    app: ${name}
spec:
  replicas: ${replicas}
  selector:
    matchLabels:
      app: ${name}
  template:
    metadata:
      labels:
        app: ${name}
    spec:
      containers:
        - name: ${name}
          image: ${image}
          ports:
            - containerPort: ${port}
---
apiVersion: v1
kind: Service
metadata:
  name: ${name}
spec:
  selector:
    app: ${name}
  ports:
    - port: ${port}
      targetPort: ${port}
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: ${name}
spec:
  rules:
    - backendRefs:
        - name: ${name}
          port: ${port}
`,
		Parameters: mustParams([]apptemplate.Parameter{
			{Name: "name", Label: "Name", Type: "string", Required: true, Default: "web"},
			{Name: "image", Label: "Image", Type: "string", Required: true, Default: "nginx:1.27"},
			{Name: "replicas", Label: "Replicas", Type: "number", Default: "2"},
			{Name: "port", Label: "Port", Type: "number", Default: "8080"},
		}),
	},
	{
		Name:        "scheduled-job",
		Title:       "Scheduled job",
		Description: "A CronJob that runs one container on a schedule.",
		Manifests: `apiVersion: batch/v1
kind: CronJob
metadata:
  name: ${name}
spec:
  schedule: "${schedule}"
  jobTemplate:
    spec:
      template:
        spec:
          containers:
            - name: ${name}
              image: ${image}
          restartPolicy: OnFailure
`,
		Parameters: mustParams([]apptemplate.Parameter{
			{Name: "name", Label: "Name", Type: "string", Required: true, Default: "job"},
			{Name: "image", Label: "Image", Type: "string", Required: true, Default: "busybox:1.36"},
			{Name: "schedule", Label: "Schedule", Type: "string", Required: true, Default: "0 * * * *"},
		}),
	},
	{
		Name:        "stateful-store",
		Title:       "Stateful store",
		Description: "A StatefulSet with a volume claim template and a headless Service.",
		Manifests: `apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: ${name}
spec:
  serviceName: ${name}
  replicas: ${replicas}
  selector:
    matchLabels:
      app: ${name}
  template:
    metadata:
      labels:
        app: ${name}
    spec:
      containers:
        - name: ${name}
          image: ${image}
          volumeMounts:
            - name: data
              mountPath: /data
  volumeClaimTemplates:
    - metadata:
        name: data
      spec:
        accessModes: ["ReadWriteOnce"]
        resources:
          requests:
            storage: ${storage}
---
apiVersion: v1
kind: Service
metadata:
  name: ${name}
spec:
  clusterIP: None
  selector:
    app: ${name}
  ports:
    - port: 5432
`,
		Parameters: mustParams([]apptemplate.Parameter{
			{Name: "name", Label: "Name", Type: "string", Required: true, Default: "store"},
			{Name: "image", Label: "Image", Type: "string", Required: true, Default: "postgres:16"},
			{Name: "replicas", Label: "Replicas", Type: "number", Default: "1"},
			{Name: "storage", Label: "Storage", Type: "string", Default: "10Gi"},
		}),
	},
}
