package base

import (
	"io"
)

type Emitter struct {
	pipeReader *io.PipeReader
	pipeWriter *io.PipeWriter
	closed     bool
}

func (em *Emitter) IsClosed() bool {
	return em.closed
}

func (em *Emitter) Read(b []byte) (int, error) {
	n, err := em.pipeReader.Read(b)
	if err != nil {
		em.Close()
		return 0, err
	}
	return n, nil
}

func (em *Emitter) Write(b []byte) (int, error) {
	n, err := em.pipeWriter.Write(b)
	if err != nil {
		em.Close()
		return 0, err
	}
	return n, nil
}

func (em *Emitter) WriteString(s string) (int, error) {
	return em.Write([]byte(s))
}

func (em *Emitter) Close() error {
	em.closed = true
	em.pipeReader.Close()
	em.pipeWriter.Close()
	return nil
}

func NewEmitter(reader *io.PipeReader, writer *io.PipeWriter) *Emitter {
	return &Emitter{
		pipeReader: reader,
		pipeWriter: writer,
		closed:     false,
	}
}
