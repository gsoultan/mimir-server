import { useMutation } from "@connectrpc/connect-query";
import { Alert, Button, Card, Group, Radio, Stack, Stepper, Text, Textarea, TextInput } from "@mantine/core";
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

export const Route = createFileRoute("/_app/models/new")({
  // kind picks the model type to start on, such as "mim" or "language-model".
  validateSearch: z.object({ kind: z.string().optional() }),
  loader: ({ context }) => context.queryClient.ensureQueryData(listArchitectures),
  component: AddModel,
});

type Picked = { file: File; bytes: Uint8Array; format: Format; tensors?: number; parameters?: number };

// Model types that take a whole model, rather than the weights of one the
// server builds, also take ONNX and GGUF files and convert them.
const takes = (whole: boolean) => whole ? ".mim, ONNX or GGUF" : ".mim";

function AddModel() {
  const { data } = useSuspenseQuery(listArchitectures);
  const navigate = useNavigate();
  const { queryClient } = Route.useRouteContext();
  const [step, setStep] = useState(0);
  const { kind } = Route.useSearch();
  const [arch, setArch] = useState(data.architectures.find((a) => a.name === kind)?.name ?? data.architectures[0]?.name ?? "");
  const [picked, setPicked] = useState<Picked>();
  const [fileError, setFileError] = useState<string>();
  const [reading, setReading] = useState(false);
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const whole = !data.architectures.find((a) => a.name === arch)?.trainable;

  const create = useMutation(ModelService.method.createModel, {
    onSuccess: async (res) => {
      await queryClient.invalidateQueries({ queryKey: allModelQueries });
      notifications.show({ color: "teal", icon: <IconCheck size={18} />, message: `${res.model?.name} is ready.` });
      await navigate({ to: "/try", search: { model: res.model?.id } });
    },
  });

  async function pick(file: File) {
    setFileError(undefined);
    setReading(true);
    const r = await readFile(file);
    setReading(false);
    if ("error" in r) return setFileError(r.error);
    if (!r.inspection.ok) return setFileError(r.inspection.reason);
    if (r.inspection.format !== "mim" && !whole) {
      return setFileError(`This model type takes a .mim file. To add ${r.inspection.format === "gguf" ? "a GGUF language model, go back and pick Language model" : "an ONNX model, go back and pick Mimir Model file"}.`);
    }
    setPicked({ file, bytes: r.bytes, ...r.inspection });
    if (!name) setName(file.name.replace(/\.(mim|onnx|gguf)$/i, ""));
  }

  return (
    <Page title="Add a model" intro="Three short steps. Nothing is saved until the end.">
      <Card withBorder radius="lg" padding="xl">
        <Stepper active={step} onStepClick={(s) => s < step && setStep(s)} size="sm">
          <Stepper.Step label="What kind" description="Pick the model type">
            <Radio.Group value={arch} onChange={setArch} label="What does this model do?" mt="md">
              <Stack gap="sm" mt="sm">
                {data.architectures.map((a) => (
                  <Radio.Card key={a.name} value={a.name} radius="md" p="md" className="data-[checked]:border-[var(--mantine-color-teal-7)]">
                    <Group wrap="nowrap" align="flex-start">
                      <Radio.Indicator />
                      <div>
                        <Text fw={600}>{a.title}</Text>
                        <Text size="sm" c="dimmed">{a.description}</Text>
                      </div>
                    </Group>
                  </Radio.Card>
                ))}
              </Stack>
            </Radio.Group>
          </Stepper.Step>

          <Stepper.Step label="The file" description={whole ? "Upload the model" : "Upload the weights"}>
            <Dropzone
              mt="md"
              onDrop={(files) => files[0] && pick(files[0])}
              onReject={() => setFileError(`Pick a single ${takes(whole)} file.`)}
              maxFiles={1}
              multiple={false}
              loading={reading}
              radius="lg"
            >
              <Stack align="center" gap={4} py="xl" className="pointer-events-none">
                <IconFileUpload size={40} stroke={1.5} />
                <Text fw={600}>Drop your {takes(whole)} file here</Text>
                <Text size="sm" c="dimmed">or click to choose one{whole && ". ONNX and GGUF models become .mim files when you add them."}</Text>
              </Stack>
            </Dropzone>
            {fileError && <Alert mt="md" color="red" icon={<IconAlertCircle />}>{fileError}</Alert>}
            {picked && (
              <Alert mt="md" color="teal" icon={<IconCheck />}>
                {picked.file.name} · {bytes(picked.file.size)}
                {picked.parameters !== undefined
                  ? ` · ${picked.parameters.toLocaleString()} numbers learned`
                  : picked.tensors !== undefined ? ` · ${picked.tensors.toLocaleString()} weight tables` : ""}
                {picked.format !== "mim" && ` · becomes a .mim file when you add it`}
              </Alert>
            )}
          </Stepper.Step>

          <Stepper.Step label="Name it" description="So you can find it">
            <Stack mt="md">
              <TextInput label="Name" required maxLength={100} value={name} onChange={(e) => setName(e.currentTarget.value)} data-autofocus />
              <Textarea label="Description" description="Optional. What is it for?" maxLength={500} autosize minRows={2} value={description} onChange={(e) => setDescription(e.currentTarget.value)} />
              {create.error && <Alert color="red" icon={<IconAlertCircle />}>{message(create.error)}</Alert>}
            </Stack>
          </Stepper.Step>
        </Stepper>

        <Group justify="space-between" mt="xl">
          <Button variant="default" onClick={() => setStep(step - 1)} disabled={step === 0 || create.isPending}>Back</Button>
          {step < 2 ? (
            <Button onClick={() => setStep(step + 1)} disabled={(step === 0 && !arch) || (step === 1 && !picked)}>Next</Button>
          ) : (
            <Button
              loading={create.isPending}
              disabled={!name.trim() || !picked}
              onClick={() => picked && create.mutate({ name, description, architecture: arch, weights: picked.bytes })}
            >
              Add model
            </Button>
          )}
        </Group>
      </Card>
    </Page>
  );
}
