"""Trains the server's table network in PyTorch on rows TestAccuracy
dumped, with the same split, network and settings, and prints held-out
accuracy, so the two can be compared:

    MIMIR_ACCURACY_CSV=adult.csv MIMIR_ACCURACY_DUMP=/tmp/adult \
        go test -run TestAccuracy -v ./internal/training
    python3 tools/accuracy/train.py /tmp/adult [seeds]
"""
import json
import sys

import numpy as np
import torch
import torch.nn as nn

torch.set_num_threads(4)
d = sys.argv[1]
seeds = int(sys.argv[2]) if len(sys.argv) > 2 else 3
meta = json.load(open(f"{d}/meta.json"))
f, c = meta["features"], meta["classes"]
x = torch.from_numpy(np.fromfile(f"{d}/x.f32", np.float32).reshape(-1, f))
y = torch.from_numpy(np.fromfile(f"{d}/y.i32", np.int32).astype(np.int64))
tr = torch.from_numpy(np.fromfile(f"{d}/train.i32", np.int32).astype(np.int64))
te = torch.from_numpy(np.fromfile(f"{d}/test.i32", np.int32).astype(np.int64))
mean, std = torch.tensor(meta["mean"]), torch.tensor(meta["std"])
x = (x - mean) / std
epochs, batch, lr = meta["epochs"], meta["batch"], meta["lr"]
hidden = meta["hidden"]

total = 0
for seed in range(1, seeds + 1):
    torch.manual_seed(seed)
    layers, width = [], f
    for h in hidden:
        layers += [nn.Linear(width, h), nn.ReLU()]
        width = h
    model = nn.Sequential(*layers, nn.Linear(width, c))
    opt = torch.optim.AdamW(model.parameters(), lr=lr, weight_decay=0.01)
    sched = torch.optim.lr_scheduler.CosineAnnealingLR(opt, epochs, eta_min=lr / 20)
    lossf = nn.CrossEntropyLoss()
    accs = []
    for epoch in range(epochs):
        model.train()
        for idx in tr[torch.randperm(len(tr))].split(batch):
            loss = lossf(model(x[idx]), y[idx])
            opt.zero_grad()
            loss.backward()
            opt.step()
        sched.step()
        model.eval()
        with torch.no_grad():
            accs.append((model(x[te]).argmax(1) == y[te]).float().mean().item())
    total += accs[-1]
    print(f"seed {seed}: accuracy {accs[-1]:.4f}, by epoch " + " ".join(f"{a:.4f}" for a in accs), flush=True)
print(f"torch {torch.__version__}: epochs={epochs} batch={batch} lr={lr} mean accuracy {total / seeds:.4f}")
