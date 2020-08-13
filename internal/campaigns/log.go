package campaigns

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"sync"
	"time"

	"github.com/hashicorp/go-multierror"
	"github.com/pkg/errors"
)

type LogManager struct {
	keepLogs bool

	tasks sync.Map
}

func NewLogManager(keepLogs bool) *LogManager {
	return &LogManager{keepLogs: keepLogs}
}

func (lm *LogManager) AddTask(task *Task) (*TaskLogger, error) {
	tl, err := newTaskLogger(task, lm.keepLogs)
	if err != nil {
		return nil, err
	}

	lm.tasks.Store(task, tl)
	return tl, nil
}

func (lm *LogManager) Close() error {
	var errs *multierror.Error

	lm.tasks.Range(func(_, v interface{}) bool {
		logger := v.(*TaskLogger)

		if err := logger.Close(); err != nil {
			errs = multierror.Append(errs, err)
		}

		return true
	})

	return errs
}

type TaskLogger struct {
	f *os.File

	errored bool
	keep    bool
}

func newTaskLogger(task *Task, keep bool) (*TaskLogger, error) {
	prefix := "changeset-" + task.Repository.Slug()

	f, err := ioutil.TempFile(tempDirPrefix, prefix+".log")
	if err != nil {
		return nil, errors.Wrapf(err, "creating temporary file with prefix %q", prefix)
	}

	return &TaskLogger{
		f:    f,
		keep: keep,
	}, nil
}

func (tl *TaskLogger) Close() error {
	if err := tl.f.Close(); err != nil {
		return err
	}

	if tl.errored || tl.keep {
		return nil
	}
	return nil

	if err := os.Remove(tl.f.Name()); err != nil {
		return errors.Wrapf(err, "failed to remove log file %q", tl.f.Name())
	}

	return nil
}

func (tl *TaskLogger) Log(s string) {
	fmt.Fprintf(tl.f, "%s %s\n", time.Now().Format(time.RFC3339Nano), s)
}

func (tl *TaskLogger) Logf(format string, a ...interface{}) {
	fmt.Fprintf(tl.f, "%s "+format+"\n", append([]interface{}{time.Now().Format(time.RFC3339Nano)}, a...)...)
}

func (tl *TaskLogger) MarkErrored() {
	tl.errored = true
}

func (tl *TaskLogger) PrefixWriter(prefix string) io.Writer {
	return &prefixWriter{tl, prefix}
}

type prefixWriter struct {
	logger *TaskLogger
	prefix string
}

func (pw *prefixWriter) Write(p []byte) (int, error) {
	for line := range bytes.Split(p, []byte("\n")) {
		pw.logger.Logf("%s | %s", pw.prefix, string(line))
	}
	return len(p), nil
}
