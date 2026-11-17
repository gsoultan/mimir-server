import { useMutation } from "@connectrpc/connect-query";
import { Alert, Badge, Button, Card, Group, Progress, Radio, Stack, Stepper, Text, Textarea, TextInput } from "@mantine/core";
import { Dropzone } from "@mantine/dropzone";
import { notifications } from "@mantine/notifications";
import { IconAlertCircle, IconCheck, IconFileUpload } from "@tabler/icons-react";
import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { z } from "zod";

import { message } from "../../../api/client";
import { ModelService } from "../../../api/gen/mimir/v1/model_pb";
import { allModelQueries, listArchitectures } from "../../../api/queries";
import { Page } from "../../../components/Page";
import { bytes } from "../../../lib/format";
import type { Format } from "../../../lib/inspect";
import { readFile } from "../../../lib/read-file";
import { upload } from "../../../lib/upload";
import { t } from "../../../i18n";

export const Route = createFileRoute("/_app/models/new")({
  // kind picks the model type to start on, such as "mim" or "language-model".
  validateSearch: z.object({ kind: z.string().optional() }),
  loader: ({ context }) => context.queryClient.ensureQueryData(listArchitectures),
  component: AddModel,
});

// A custom model's file is whatever its own code reads, so it is taken as
// it is (format "own"). The file is sent from disk when it is added.
type Picked = { file: File; format: Format | "own"; tensors?: number; parameters?: number };

// shapeOf reads "28" or "3 x 64 x 64" as a shape.
const shapeOf = (s: string) => {
  const parts = s.split(/[\s,x×*]+/).filter(Boolean).map(Number);
  return parts.length > 0 && parts.every((n) => Number.isInteger(n) && n > 0) ? parts.map(BigInt) : undefined;
};

// Model types that take a whole model, rather than the weights of one the
// server builds, also take ONNX and GGUF files and convert them.
const takes = (whole: boolean) => whole ? t(".mim, ONNX or GGUF") : ".mim";

function AddModel() {
  const { data } = useSuspenseQuery(listArchitectures);
  const navigate = useNavigate();
  const { queryClient } = Route.useRouteContext();
  const [step, setStep] = useState(0);
  const { kind } = Route.useSearch();
  // Whole model files (Mimir Model, ONNX, GGUF) are what most people
  // upload; weights for a kind mimir teaches come after, folded away.
  const wholeKinds = data.architectures.filter((a) => !a.trainable);
  const weightKinds = data.architectures.filter((a) => a.trainable);
  const [arch, setArch] = useState(data.architectures.find((a) => a.name === kind)?.name ?? wholeKinds[0]?.name ?? data.architectures[0]?.name ?? "");
  const [more, setMore] = useState(weightKinds.some((a) => a.name === kind));
  const [picked, setPicked] = useState<Picked>();
  const [fileError, setFileError] = useState<string>();
  const [reading, setReading] = useState(false);
  // sent is the fraction of the file sent while it is being sent.
  const [sent, setSent] = useState<number>();
  const [sendError, setSendError] = useState<string>();
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const picked0 = data.architectures.find((a) => a.name === arch);
  const custom = !!picked0?.custom;
  const whole = !picked0?.trainable;
  const [shape, setShape] = useState("");
  const [labels, setLabels] = useState("");
  const labelList = labels.split("\n").map((l) => l.trim()).filter(Boolean);
  const specOk = !custom || (!!shapeOf(shape) && (picked0?.output === "number" || labelList.length >= 2));

  const create = useMutation(ModelService.method.createModel, {
    onSuccess: async (res) => {
      await queryClient.invalidateQueries({ queryKey: allModelQueries });
      notifications.show({ color: "teal", icon: <IconCheck size={18} />, message: t("{name} is ready.", { name: res.model?.name }) });
      await navigate({ to: "/try", search: { model: res.model?.id } });
    },
  });

  async function pick(file: File) {
    setFileError(undefined);
    if (custom) {
      setPicked({ file, format: "own" });
      if (!name) setName(file.name.replace(/\.[^.]+$/, ""));
      return;
    }
    setReading(true);
    const r = await readFile(file);
    setReading(false);
    if ("error" in r) return setFileError(r.error);
    if (!r.inspection.ok) return setFileError(r.inspection.reason);
    if (r.inspection.format !== "mim" && !whole) {
      return setFileError(t("This model type takes a .mim file. To add {n1}.", { n1: r.inspection.format === "gguf" ? t("a GGUF language model, go back and pick Language model") : t("an ONNX model, go back and pick Mimir Model file") }));
    }
    setPicked({ file, ...r.inspection });
    if (!name) setName(file.name.replace(/\.(mim|onnx|gguf)$/i, ""));
  }

  // add sends the file, then adds the model from it.
  async function add(p: Picked) {
    setSendError(undefined);
    setSent(0);
    let id: string;
    try {
      id = await upload(p.file, setSent);
    } catch (err) {
      setSendError(message(err));
      return;
    } finally {
      setSent(undefined);
    }
    create.mutate({ name, description, architecture: arch, upload: id, ...(custom ? { inputShape: shapeOf(shape), labels: labelList } : {}) });
  }
  const busy = sent !== undefined || create.isPending;

  return (
    <Page title={t("Add a model")} crumbs={[{ label: t("Models"), to: "/models" }, { label: t("Upload a file") }]} intro={t("Three short steps. Nothing is saved until the end.")}>
      <Card withBorder radius="lg" padding="xl">
        <Stepper active={step} onStepClick={(s) => s < step && setStep(s)} size="sm">
          <Stepper.Step label={t("What kind")} description={t("Pick the model type")}>
            <Radio.Group value={arch} onChange={(v) => { setArch(v); setPicked(undefined); }} label={t("What does this model do?")} mt="md">
              <Stack gap="sm" mt="sm">
                {[...wholeKinds, ...(more ? weightKinds : [])].map((a) => (
                  <Radio.Card key={a.name} value={a.name} radius="md" p="md">
                    <Group wrap="nowrap" align="flex-start">
                      <Radio.Indicator />
                      <div>
                        <Group gap="xs"><Text fw={600}>{a.title}</Text>{a.custom && <Badge size="sm" variant="light" color="grape">{t("Your own code")}</Badge>}</Group>
                        <Text size="sm" c="dimmed">{a.description}</Text>
                      </div>
                    </Group>
                  </Radio.Card>
                ))}
                {weightKinds.length > 0 && (
                  <Button variant="subtle" size="compact-sm" className="self-start" onClick={() => setMore(!more)}>
                    {more ? t("Show fewer") : t("Only the weights of a model kind mimir teaches? Show {weightKinds} more", { weightKinds: weightKinds.length })}
                  </Button>
                )}
              </Stack>
            </Radio.Group>
          </Stepper.Step>

          <Stepper.Step label={t("The file")} description={whole || custom ? t("Upload the model") : t("Upload the weights")}>
            <Dropzone
              mt="md"
              onDrop={(files) => files[0] && pick(files[0])}
              onReject={() => setFileError(t("Pick a single {whole} file.", { whole: takes(whole) }))}
              maxFiles={1}
              multiple={false}
              loading={reading}
              radius="lg"
            >
              <Stack align="center" gap={4} py="xl" className="pointer-events-none">
                <IconFileUpload size={40} stroke={1.5} />
                <Text fw={600}>{t("Drop your {what} file here", { what: custom ? t("model's") : takes(whole) })}</Text>
                <Text size="sm" c="dimmed">{t("or click to choose one")}{custom ? t(". It is kept as it is, for your own code to read.") : whole && t(". ONNX and GGUF models become .mim files when you add them.")}</Text>
              </Stack>
            </Dropzone>
            {fileError && <Alert mt="md" color="red" icon={<IconAlertCircle />}>{fileError}</Alert>}
            {picked && (
              <Alert mt="md" color="teal" icon={<IconCheck />}>
                {picked.file.name} · {bytes(picked.file.size)}
                {picked.parameters !== undefined
                  ? t(" · {n1} numbers learned", { n1: picked.parameters.toLocaleString() })
                  : picked.tensors !== undefined ? t(" · {n1} weight tables", { n1: picked.tensors.toLocaleString() }) : ""}
                {picked.format !== "mim" && picked.format !== "own" && t(" · becomes a .mim file when you add it")}
              </Alert>
            )}
          </Stepper.Step>

          <Stepper.Step label={t("Name it")} description={t("So you can find it")}>
            <Stack mt="md">
              <TextInput label={t("Name")} required maxLength={100} value={name} onChange={(e) => setName(e.currentTarget.value)} data-autofocus />
              <Textarea label={t("Description")} description={t("Optional. What is it for?")} maxLength={500} autosize minRows={2} value={description} onChange={(e) => setDescription(e.currentTarget.value)} />
              {custom && (
                <>
                  <TextInput label={t("How many numbers it reads")} description={t("One example, like 12, or 3 x 64 x 64 for a photo")} required value={shape} onChange={(e) => setShape(e.currentTarget.value)} error={shape && !shapeOf(shape) ? t("Whole numbers above 0, like 12 or 3 x 64 x 64") : undefined} />
                  {picked0?.output !== "number" && (
                    <Textarea label={picked0?.output === "ranked" ? t("The items it ranks") : t("The answers it picks from")} description={t("One per line, in the order your code scores them")} required autosize minRows={3} maxRows={10} value={labels} onChange={(e) => setLabels(e.currentTarget.value)} />
                  )}
                </>
              )}
              {sent !== undefined && (
                <div>
                  <Text size="sm" c="dimmed" mb={4}>{t("Sending")}{" "}{picked?.file.name}… {Math.floor(sent * 100)}%</Text>
                  <Progress value={sent * 100} size="sm" radius="xl" aria-label={t("Sent")} />
                </div>
              )}
              {create.isPending && <Text size="sm" c="dimmed">{picked?.format === "gguf" || picked?.format === "onnx" ? t("Converting and checking the model…") : t("Checking the model…")}</Text>}
              {(sendError ?? create.error) && <Alert color="red" icon={<IconAlertCircle />}>{sendError ?? message(create.error)}</Alert>}
            </Stack>
          </Stepper.Step>
        </Stepper>

        <Group justify="space-between" mt="xl">
          <Button variant="default" onClick={() => setStep(step - 1)} disabled={step === 0 || busy}>{t("Back")}</Button>
          {step < 2 ? (
            <Button onClick={() => setStep(step + 1)} disabled={(step === 0 && !arch) || (step === 1 && !picked)}>{t("Next")}</Button>
          ) : (
            <Button
              loading={busy}
              disabled={!name.trim() || !picked || !specOk}
              onClick={() => picked && add(picked)}
            >{t("Add model")}</Button>
          )}
        </Group>
      </Card>
    </Page>
  );
}
