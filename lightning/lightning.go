package lightning

import (
	"context"
	"fmt"
	"github.com/pingcap/dm/pkg/terror"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/pingcap/dm/dm/config"
	"github.com/pingcap/dm/dm/pb"
	"github.com/pingcap/dm/dm/unit"
	tcontext "github.com/pingcap/dm/pkg/context"
	"github.com/pingcap/dm/pkg/log"
	lightningConfig "github.com/pingcap/tidb-lightning/lightning/config"
	"github.com/pingcap/tidb-lightning/lightning/mydump"
	"github.com/pingcap/tidb-lightning/lightning/restore"
	"github.com/pingcap/tidb-lightning/lightning/web"
	"go.uber.org/zap"
)

// Lightning is used to replace Loader.
type Lightning struct {
	rc     *restore.RestoreController
	mdl    *mydump.MDLoader
	logCtx *tcontext.Context

	cfg    *config.SubTaskConfig
	ltnCfg *lightningConfig.Config
}

// NewLightning return new Lightning object.
func NewLightning(cfg *config.SubTaskConfig) *Lightning {
	logCtx := tcontext.Background().WithLogger(log.With(zap.String("task", cfg.Name), zap.String("unit", "lightning")))
	lightningCf := lightningConfig.NewConfig()

	return &Lightning{
		cfg:    cfg,
		logCtx: logCtx,
		ltnCfg: lightningCf,
	}
}

// Init implements Unit.Init
func (l *Lightning) Init(ctx context.Context) error {
	lightningCf := l.ltnCfg
	cfg := l.cfg

	// Set TiDB backend config.
	lightningCf.TikvImporter.Backend = lightningConfig.BackendTiDB
	lightningCf.TiDB.Host = cfg.To.Host
	lightningCf.TiDB.User = cfg.To.User
	lightningCf.TiDB.Port = cfg.To.Port
	lightningCf.TiDB.Psw = cfg.To.Password

	// Set black & white list config.
	lightningCf.BWList = cfg.BWList

	// Set router rules.
	lightningCf.Routes = cfg.RouteRules

	// Set MyDumper config.
	lightningCf.Mydumper.SourceDir = cfg.Dir

	// Set cocurrency size.
	lightningCf.App.IndexConcurrency = cfg.PoolSize
	lightningCf.App.TableConcurrency = cfg.PoolSize

	// Set checkpoint config.
	lightningCf.Checkpoint.Driver = lightningConfig.CheckpointDriverMySQL

	// Set misc config.
	lightningCf.App.CheckRequirements = false

	err := lightningCf.Adjust()
	fmt.Println(lightningCf.String())
	return err
}

// Process implements Unit.Process
func (l *Lightning) Process(ctx context.Context, pr chan pb.ProcessResult) {
	fmt.Println("lightning begin")
	files := CollectDirFiles(l.cfg.Dir)
	web.InitProgress()
	err := l.prepareTableFiles(files)

	mdl, err := mydump.NewMyDumpLoader(l.ltnCfg)
	if err != nil {
		fmt.Println("load fail")
		l.logCtx.L().Error("create MyDumperLoader failed", log.ShortError(err))
		pr <- pb.ProcessResult{
			Errors: []*pb.ProcessError{unit.NewProcessError(pb.ErrorType_UnknownError, err)},
		}
		return
	}
	l.mdl = mdl
	dbMetas := mdl.GetDatabases()
	procedure, err := restore.NewRestoreController(ctx, dbMetas, l.ltnCfg)
	if err != nil {
		l.logCtx.L().Error("create RestoreController failed", log.ShortError(err))
		pr <- pb.ProcessResult{
			Errors: []*pb.ProcessError{unit.NewProcessError(pb.ErrorType_UnknownError, err)},
		}
		return
	}
	l.rc = procedure
	err = l.rc.Run(ctx)
	fmt.Println(err)
	l.rc.Wait()
	if err != nil {
		l.logCtx.L().Error("error occur during restore", log.ShortError(err))
		pr <- pb.ProcessResult{
			Errors: []*pb.ProcessError{unit.NewProcessError(pb.ErrorType_UnknownError, err)},
		}
		return
	}
}

// Close implements Unit.Close
func (l *Lightning) Close() {
	l.rc.Close()
}

// Pause implements Unit.Pause
func (l *Lightning) Pause() {

}

// Resume implements Unit.Resume
func (l *Lightning) Resume(ctx context.Context, pr chan pb.ProcessResult) {

}

// Update implements Unit.Update
func (l *Lightning) Update(cfg *config.SubTaskConfig) error {
	return nil
}

// Status implements Unit.Status
func (l *Lightning) Status() interface{} {
	return nil
}

// Error implements Unit.Error
func (l *Lightning) Error() interface{} {
	return nil
}

// Type implements Unit.Type
func (l *Lightning) Type() pb.UnitType {
	return pb.UnitType_Lightning
}

// IsFreshTask implements Unit.IsFreshTask
func (l *Lightning) IsFreshTask(ctx context.Context) (bool, error) {
	return true, nil
}

func escapeName(name string) string {
	return strings.Replace(name, "`", "``", -1)
}

func generateSchemaCreateFile(dir string, schema string) error {
	file, err := os.Create(path.Join(dir, fmt.Sprintf("%s-schema-create.sql", schema)))
	if err != nil {
		return terror.ErrLoadUnitCreateSchemaFile.Delegate(err)
	}
	defer file.Close()

	_, err = fmt.Fprintf(file, "CREATE DATABASE `%s`;\n", escapeName(schema))
	return terror.ErrLoadUnitCreateSchemaFile.Delegate(err)
}

func (l *Lightning) prepareTableFiles(files map[string]struct{}) error {
	for file := range files {
		if !strings.HasSuffix(file, "-schema.sql") {
			continue
		}

		idx := strings.Index(file, "-schema.sql")
		name := file[:idx]
		fields := strings.Split(name, ".")
		if len(fields) != 2 {
			l.logCtx.L().Warn("invalid table schema file", zap.String("file", file))
			continue
		}

		db := fields[0]
		if err := generateSchemaCreateFile(l.cfg.Dir, db); err != nil {
			return err
		}

	}

	return nil
}

// CollectDirFiles gets files in path
func CollectDirFiles(path string) map[string]struct{} {
	files := make(map[string]struct{})
	filepath.Walk(path, func(path string, f os.FileInfo, err error) error {
		if err != nil {
			return nil
		}

		if f == nil {
			return nil
		}

		if f.IsDir() {
			return nil
		}

		name := strings.TrimSpace(f.Name())
		files[name] = struct{}{}
		return nil
	})

	return files
}
