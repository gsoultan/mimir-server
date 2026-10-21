import { useMutation } from "@connectrpc/connect-query";
import { Alert, Anchor, Card, SegmentedControl, Select, Stack, Text } from "@mantine/core";
import { IconDeviceLaptop, IconDownload } from "@tabler/icons-react";
import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { z } from "zod";

import { PredictService, type Architecture, type Model } from "../../api/gen/mimir/v1/model_pb";
import { listArchitectures, listModels, whoAmI } from "../../api/queries";
import { Ask, askIntro, type Answer, type Question } from "../../components/Ask";
import { Chat } from "../../components/Chat";
import { ButtonLink } from "../../components/links";
import { Page } from "../../components/Page";
import { Writer } from "../../components/Writer";
import * as device from "../../lib/device";
import { bytes } from "../../lib/format";

export const Route = createFileRoute("/_app/try")({
  validateSearch: z.object({ model: z.string().optional(), mode: z.enum(["write"]).optional(), where: z.enum(["device"]).optional() }),
  loader: ({ context }) => Promise.all([
    context.queryClient.ensureQueryData(listModels),
    context.queryClient.ensureQueryData(listArchitectures),
  ]),
  component: Try,
});

function Try() {
  const { model: selected, mode: writeMode, where } = Route.useSearch();
  const mode = writeMode ?? "chat";
  const { data: me } = useSuspenseQuery(whoAmI);
  const navigate = Route.useNavigate();
  const { data } = useSuspenseQuery(listModels);
  const { data: archs } = useSuspenseQuery(listArchitectures);
  const predict = useMutation(PredictService.method.predict);

  const model = data.models.find((m) => m.id === selected) ?? data.models[0];
  if (!model) {
    return (
      <Page title="Try a model">
        <Card withBorder radius="lg" padding="xl">
          <Text mb="md">Build or upload a model first, then come back to try it.</Text>
          <ButtonLink to="/models/build">Build a model</ButtonLink>
        </Card>
      </Page>
    );
  }
  const arch = archs.architectures.find((a) => a.name === model.architecture);
  const pickModel = (
    <Select
      label="Model"
      className="w-full max-w-sm"
      data={data.models.map((m) => ({ value: m.id, label: m.name }))}
      value={model.id}
      allowDeselect={false}
      onChange={(id) => void navigate({ search: { model: id ?? undefined, where } })}
    />
  );
  if (arch?.output === "text") {
    const modes = (
      <div className="flex flex-wrap items-end gap-3">
        <div className="w-full sm:w-80">{pickModel}</div>
        <SegmentedControl data={[{ value: "chat", label: "Chat" }, { value: "write", label: "Carry on a text" }]} value={mode} onChange={(v) => void navigate({ search: { model: model.id, mode: v === "write" ? "write" : undefined } })} />
      </div>
    );
    return (
      <Page title="Try a model" intro={mode === "chat" ? "Chat with a language model, the way your apps will." : "Start a text and see how the model carries on."}>
        {mode === "chat"
          ? <Chat key={model.id} model={model} canWrite={me.permissions.includes("models.write")} header={modes} />
          : <>{modes}<Writer key={model.id} modelId={model.id} /></>}
      </Page>
    );
  }
  // A model someone let run on devices can answer here, in the browser.
  const canDevice = device.supported && model.onDevice && !!arch;
  const onDevice = canDevice && where === "device";
  const ask = async (q: Question): Promise<Answer> => {
    if (onDevice) {
      await ensureSaved(model, arch);
      return device.answer({ id: model.id, fields: q.fields, photo: q.photo, input: q.input?.values, topK: q.topK });
    }
    const r = await predict.mutateAsync({ modelId: model.id, ...q, photo: q.photo ?? new Uint8Array() });
    return { number: r.number, value: r.value, guesses: r.guesses, ms: Number(r.durationMicros) / 1000 };
  };
  return (
    <Page title="Try a model" intro={askIntro(arch)}>
      <div className="flex flex-wrap items-end gap-3">
        <div className="w-full sm:w-80">{pickModel}</div>
        {canDevice && (
          <Stack gap={4}>
            <Text size="sm" fw={500}>Where it runs</Text>
            <SegmentedControl
              data={[{ value: "server", label: "On the server" }, { value: "device", label: "On this device" }]}
              value={onDevice ? "device" : "server"}
              onChange={(v) => void navigate({ search: { model: model.id, where: v === "device" ? "device" : undefined } })}
            />
          </Stack>
        )}
      </div>
      {onDevice && <DeviceNote model={model} />}
      <Ask key={`${model.id}-${onDevice}`} model={model} arch={arch} answer={ask} where={onDevice ? "on this device" : undefined} />
    </Page>
  );
}

// ensureSaved saves model m on this device, or its newer version.
async function ensureSaved(m: Model, arch: Architecture) {
  const saved = await device.get(m.id);
  if (saved?.model.version !== m.version) await device.save(m, arch);
}

// DeviceNote says whether the model is saved here, and what saving costs.
function DeviceNote({ model }: { model: Model }) {
  const all = device.useSaved();
  if (!all) return null;
  const saved = all.find((s) => s.model.id === model.id);
  const current = saved?.model.version === model.version;
  return (
    <Alert color={current ? "teal" : "blue"} variant="light" icon={current ? <IconDeviceLaptop /> : <IconDownload />} className="max-w-2xl">
      {current
        ? <>Saved on this device, so it answers here, even offline. <Anchor component={Link} to="/device" size="sm">Models on this device</Anchor></>
        : `${saved ? "A newer version is on the server. " : ""}The first question downloads the model (${bytes(model.sizeBytes)}) to this device. After that it answers here, even offline.`}
    </Alert>
  );
}
