import { useMutation } from "@connectrpc/connect-query";
import { Alert, Button, Card, Code, Group, SegmentedControl, Select, Stack, Text, TextInput } from "@mantine/core";
import { Dropzone, type FileWithPath } from "@mantine/dropzone";
import { notifications } from "@mantine/notifications";
import { IconAlertCircle, IconCheck } from "@tabler/icons-react";
import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { z } from "zod";

import { message } from "../../../api/client";
import { DatasetKind, DatasetService, TrainingService } from "../../../api/gen/mimir/v1/training_pb";
import { allDatasetQueries, allTrainingQueries, listDatasets, listModels, whoAmI } from "../../../api/queries";
import { Page } from "../../../components/Page";
import { useWorkerChoices, WorkerSelect } from "../../../components/Placement";
import { bytes } from "../../../lib/format";
import { count } from "../../../lib/templates";
import { t } from "../../../i18n";

export const Route = createFileRoute("/_app/models/finetune")({
  validateSearch: z.object({ model: z.string().optional() }),
  loader: ({ context }) => Promise.all([
    context.queryClient.ensureQueryData(listModels),
    context.queryClient.ensureQueryData(listDatasets),
  ]),
  component: FineTune,
});

const MaxText = 64 * 1024 * 1024;

// How long to fine-tune for, in passes over the writing.
const presets = [
  { value: "1", label: t("Quick look"), hint: t("One pass. Good for a first try.") },
  { value: "3", label: t("Normal"), hint: t("The usual choice.") },
  { value: "6", label: t("Thorough"), hint: t("Takes twice as long; follows your examples more closely.") },
];

const sample = `{"messages": [{"role": "user", "content": "Do you ship to Canada?"}, {"role": "assistant", "content": "Yes, in 5 to 7 days."}]}
{"prompt": "Summarize: The meeting moved to Tuesday.", "completion": "Meeting now Tuesday."}
{"text": "A passage written the way you write."}`;

type Upload = { name: string; size: number; bytes: Uint8Array; summary: string };

// describe says what a file of writing holds, roughly: the server does the
// real reading.
function describe(text: string) {
  const trimmed = text.trimStart();
  if (trimmed.startsWith("{")) return t("JSON Lines, {length}", { length: count(trimmed.split(/\r?\n/).filter((l) => l.trim()).length, "example") });
  return t("Plain text, {length}", { length: count(trimmed.split(/\r?\n\s*\r?\n/).filter((p) => p.trim()).length, "paragraph") });
}

// FineTune teaches a language model the way someone writes or answers,
// with small LoRA adapters stored next to the model.
function FineTune() {
  const search = Route.useSearch();
  const navigate = useNavigate();
  const { queryClient } = Route.useRouteContext();
  const { data: models } = useSuspenseQuery(listModels);
  const { data: sets } = useSuspenseQuery(listDatasets);
  const { data: me } = useSuspenseQuery(whoAmI);
  const workers = useWorkerChoices(me.permissions.includes("models.write"));
  const languageModels = models.models.filter((m) => m.architecture === "language-model");
  const writings = sets.datasets.filter((d) => d.kind === DatasetKind.TEXT);

  const [base, setBase] = useState(search.model && languageModels.some((m) => m.id === search.model) ? search.model : languageModels[0]?.id ?? "");
  const from = languageModels.find((m) => m.id === base);
  const [source, setSource] = useState<"upload" | "existing">("upload");
  const [upload, setUpload] = useState<Upload>();
  const [fileError, setFileError] = useState("");
  const [existing, setExisting] = useState(writings[0]?.id ?? "");
  const [name, setName] = useState("");
  const [epochs, setEpochs] = useState("3");
  const [runOn, setRunOn] = useState("");

  const pick = async (files: FileWithPath[]) => {
    const f = files[0];
    if (!f) return;
    if (f.size > MaxText) {
      setFileError(t("That file is {size}; writing up to 64 MB works here.", { size: bytes(f.size) }));
      return;
    }
    const b = new Uint8Array(await f.arrayBuffer());
    setFileError("");
    setUpload({ name: f.name, size: f.size, bytes: b, summary: `${bytes(f.size)} · ${describe(new TextDecoder().decode(b))}` });
  };

  const createJob = useMutation(TrainingService.method.createJob, {
    onSuccess: async (res) => {
      await queryClient.invalidateQueries({ queryKey: allTrainingQueries });
      notifications.show({ color: "teal", icon: <IconCheck size={18} />, message: t("Fine-tuning has started. You can watch it learn.") });
      await navigate({ to: "/training/$id", params: { id: res.job!.id } });
    },
  });
  const createDataset = useMutation(DatasetService.method.createDataset, {
    onSuccess: async (res) => {
      await queryClient.invalidateQueries({ queryKey: allDatasetQueries });
      if (res.dataset) teach(res.dataset.id);
    },
  });
  const modelName = name.trim() || t("{n1} (your writing)", { n1: from?.name ?? t("Model") });
  // Zero settings let the server pick what suits a fine-tune.
  const teach = (datasetId: string) => createJob.mutate({
    name: modelName, architecture: "language-model", datasetId, baseModelId: base, workerId: runOn,
    settings: { epochs: Number(epochs), batchSize: 0, learningRate: 0, holdOut: 0 },
  });
  const start = () => {
    if (source === "existing") teach(existing);
    else if (upload) createDataset.mutate({ name: upload.name, kind: DatasetKind.TEXT, text: upload.bytes });
  };
  const busy = createDataset.isPending || createJob.isPending;
  const error = createDataset.error ?? createJob.error;

  if (languageModels.length === 0) {
    return (
      <Page title={t("Fine-tune a language model")} crumbs={[{ label: t("Build a model"), to: "/models/build" }, { label: "Fine-tune" }]} intro={t("Teach a language model how you write and answer.")}>
        <Alert color="blue">{t("Add a language model first: upload a GGUF file under")}{" "}<Link to="/models/new">{t("Add a model")}</Link>.</Alert>
      </Page>
    );
  }

  return (
    <Page
      title={t("Fine-tune a language model")}
      crumbs={[{ label: t("Build a model"), to: "/models/build" }, { label: "Fine-tune" }]}
      intro={t("Teach a language model your documents, or how you answer, from your own examples. It learns small adapters next to the model, a few megabytes, so the model itself stays as it is and you can keep several fine-tunes of it.")}
    >
      <div className="grid gap-6 lg:grid-cols-[minmax(0,1fr)_360px]">
        <Card withBorder radius="lg" padding="lg">
          <Stack>
            <Select
              label={t("Language model")}
              description={t("The model to start from. A fine-tune of a fine-tune keeps learning from where that one left off.")}
              data={languageModels.map((m) => ({ value: m.id, label: m.baseModelName ? t("{name} (fine-tune of {base})", { name: m.name, base: m.baseModelName }) : m.name }))}
              value={base}
              onChange={(v) => v && setBase(v)}
              allowDeselect={false}
            />
            {writings.length > 0 && (
              <SegmentedControl
                value={source}
                onChange={(v) => setSource(v as "upload" | "existing")}
                data={[{ value: "upload", label: t("Upload writing") }, { value: "existing", label: t("Use earlier writing") }]}
              />
            )}
            {source === "existing" ? (
              <Select
                label={t("Writing")}
                data={writings.map((d) => ({ value: d.id, label: `${d.name} · ${count(d.rows, "example")}` }))}
                value={existing}
                onChange={(v) => v && setExisting(v)}
                allowDeselect={false}
              />
            ) : (
              <>
                <Dropzone
                  onDrop={pick}
                  onReject={() => setFileError(t("Pick one .txt, .md or .jsonl file."))}
                  maxFiles={1}
                  multiple={false}
                  accept={["text/plain", "text/markdown", "application/json", "application/x-ndjson", ".txt", ".md", ".jsonl", ".json"]}
                  radius="md"
                >
                  <Stack align="center" gap={2} py="md" className="pointer-events-none">
                    <Text fw={600}>{upload ? upload.name : t("Drop a .txt, .md or .jsonl file here")}</Text>
                    <Text size="sm" c="dimmed">{upload ? upload.summary : t("or click to choose one")}</Text>
                  </Stack>
                </Dropzone>
                {fileError && <Alert color="red" icon={<IconAlertCircle />}>{fileError}</Alert>}
              </>
            )}
            <TextInput label={t("Name of the fine-tune")} placeholder={modelName} maxLength={100} value={name} onChange={(e) => setName(e.currentTarget.value)} />
            <div>
              <Text size="sm" fw={500} mb={4}>{t("How long")}</Text>
              <SegmentedControl fullWidth value={epochs} onChange={setEpochs} data={presets.map((p) => ({ value: p.value, label: p.label }))} />
              <Text size="xs" c="dimmed" mt={4}>{presets.find((p) => p.value === epochs)?.hint}</Text>
            </div>
            {workers.length > 0 && (
              <WorkerSelect
                workers={workers}
                value={runOn}
                onChange={setRunOn}
                forTraining
                label={t("Train on")}
                description={t("A worker with a GPU, such as a rented one, fine-tunes much faster. Models up to 1 GB go to workers; larger ones train here.")}
              />
            )}
            {error && <Alert color="red" icon={<IconAlertCircle />}>{message(error)}</Alert>}
            <Group justify="flex-end">
              <Button loading={busy} disabled={!base || (source === "upload" ? !upload : !existing)} onClick={start}>{t("Start fine-tuning")}</Button>
            </Group>
          </Stack>
        </Card>
        <Card withBorder radius="lg" padding="lg">
          <Text fw={700} mb={4}>{t("What to upload")}</Text>
          <Text size="sm" c="dimmed" mb="sm">{t("Plain text, such as your documents or notes, teaches it what you know and how you write. For answers in your words, upload JSON Lines, one example a line, as OpenAI's fine-tuning takes them:")}</Text>
          <Code block className="whitespace-pre-wrap break-all">{sample}</Code>
          <Text size="sm" c="dimmed" mt="sm">{t("In a chat it learns the assistant's answers; after a prompt, the completion. A few dozen good examples go a long way. The fine-tune answers in Chat and from your apps like any language model, and makes embeddings at /v1/embeddings.")}</Text>
        </Card>
      </div>
    </Page>
  );
}
