// The starting project for a model written in Go and run on a worker.

export const workerModule = "github.com/gsoultan/mimir-server/worker";

// mainGo is the starting main.go for an architecture called name, on the
// server at server.
export function mainGo(name: string, server: string, what: string) {
  return `package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"

	"${workerModule}"
)

// model answers ${what}.
type model struct{}

// Predict answers one example. in is what people send, as numbers.
// m.Weights is what Train returned, or the file you uploaded.
func (model) Predict(ctx context.Context, m *worker.Model, in []float32) (worker.Prediction, error) {
	// Return one score per label in m.Labels as Probabilities, or plain
	// numbers as Output.
	return worker.Prediction{}, errors.New("predict: not written yet")
}

// Train teaches it from the Teach page. Delete it if you upload weights
// you trained elsewhere instead.
func (model) Train(ctx context.Context, t worker.TrainTask, progress func(worker.Progress)) (worker.Trained, error) {
	// t.Dataset.CSV is the table, and t.Dataset.Target names the column
	// to learn. Call progress after each pass so the page can show it.
	return worker.Trained{}, errors.New("train: not written yet")
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	err := worker.Run(ctx, worker.Config{
		Server: "${server}",
		Token:  os.Getenv("MIMIR_WORKER_TOKEN"),
		Name:   "${name}",
	}, map[string]worker.Handler{"${name}": model{}})
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}
`;
}

// setup is the commands that start the project in a new folder.
export function setup(name: string) {
  return `mkdir ${name} && cd ${name}
go mod init ${name}
go get ${workerModule}@latest`;
}
