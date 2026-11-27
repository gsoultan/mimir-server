import { useMutation } from "@connectrpc/connect-query";
import { Alert, Button, Card, Group, Modal, Select, Stack, Text, Textarea, TextInput } from "@mantine/core";
import { IconAlertCircle, IconFolder, IconPlus } from "@tabler/icons-react";
import { useQueryClient, useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useState } from "react";

import { message } from "../../../api/client";
import { DocumentService } from "../../../api/gen/mimir/v1/document_pb";
import { allDocumentQueries, listCollections, listModels, whoAmI } from "../../../api/queries";
import { AnchorLink, ButtonLink } from "../../../components/links";
import { Row, RowList } from "../../../components/ui";
import { Page } from "../../../components/Page";
import { bytes } from "../../../lib/format";
import { t } from "../../../i18n";

export const Route = createFileRoute("/_app/documents/")({
  loader: ({ context }) => Promise.all([
    context.queryClient.ensureQueryData(listCollections),
    context.queryClient.ensureQueryData(listModels),
  ]),
  component: Collections,
});

function Collections() {
  const { data } = useSuspenseQuery(listCollections);
  const { data: models } = useSuspenseQuery(listModels);
  const { data: me } = useSuspenseQuery(whoAmI);
  const canWrite = me.permissions.includes("models.write");
  const language = models.models.filter((m) => m.language).map((m) => ({ value: m.id, label: m.name }));
  const [adding, setAdding] = useState(false);

  return (
    <Page
      title={t("Ask your documents")}
      intro={t("Put handbooks, policies and notes in a collection, then ask questions in plain words. Answers show which passages they came from, and nothing leaves this server.")}
      actions={canWrite && data.collections.length > 0 && <Button leftSection={<IconPlus size={18} />} onClick={() => setAdding(true)}>{t("New collection")}</Button>}
    >
      {data.collections.length === 0 ? (
        <Card withBorder padding="xl" className="text-center">
          <Text fw={650}>{t("No collections yet")}</Text>
          <Text c="dimmed" mb="md">
            {language.length === 0
              ? t("Add a language model first, such as a Llama, Qwen or Gemma file. It reads your documents and writes the answers.")
              : canWrite ? t("Make a collection, add your files, and ask away.") : t("Someone who can add models makes collections; you can ask them here.")}
          </Text>
          {canWrite && (language.length > 0
            ? <Group justify="center"><Button leftSection={<IconPlus size={18} />} onClick={() => setAdding(true)}>{t("New collection")}</Button></Group>
            : <Group justify="center"><ButtonLink variant="default" to="/models/new">{t("Upload a language model")}</ButtonLink></Group>)}
        </Card>
      ) : (
        <RowList>
          {data.collections.map((c) => (
            <Row
              key={c.id}
              icon={<IconFolder size={20} />}
              title={<AnchorLink to="/documents/$id" params={{ id: c.id }} c="inherit" fw={650}>{c.name}</AnchorLink>}
              detail={t("{documents} · {bytes} · answered by {model}", { documents: c.documents === 1 ? t("1 document") : t("{n} documents", { n: c.documents }), bytes: bytes(c.bytes), model: c.languageModelName || t("a deleted model") })}
            >
              <ButtonLink size="xs" variant="default" to="/documents/$id" params={{ id: c.id }}>{t("Ask")}</ButtonLink>
            </Row>
          ))}
        </RowList>
      )}
      <NewCollection open={adding} onClose={() => setAdding(false)} models={language} />
    </Page>
  );
}

function NewCollection({ open, onClose, models }: { open: boolean; onClose: () => void; models: { value: string; label: string }[] }) {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const [name, setName] = useState("");
  const [embedding, setEmbedding] = useState<string | null>(models.at(0)?.value ?? null);
  const [answering, setAnswering] = useState<string | null>(null);
  const [instructions, setInstructions] = useState("");
  const create = useMutation(DocumentService.method.createCollection, {
    onSuccess: async (res) => {
      await queryClient.invalidateQueries({ queryKey: allDocumentQueries });
      close();
      await navigate({ to: "/documents/$id", params: { id: res.collection!.id } });
    },
  });
  const close = () => {
    setName(""); setInstructions(""); setAnswering(null); create.reset();
    onClose();
  };

  return (
    <Modal opened={open} onClose={close} title={t("New collection")} radius="lg" size="lg" centered>
      <Stack gap="md">
        {create.error && <Alert color="red" icon={<IconAlertCircle />}>{message(create.error)}</Alert>}
        <TextInput label={t("Name")} placeholder={t("Shop handbook")} maxLength={100} value={name} onChange={(e) => setName(e.currentTarget.value)}
          description={t("Apps that speak the OpenAI API ask it by this name.")} data-autofocus />
        <Select label={t("Model that answers")} data={models} value={answering ?? embedding} onChange={setAnswering} allowDeselect={false} searchable
          description={t("It writes the answers from the passages found. You can change it later.")} />
        <Select label={t("Model that reads the documents")} data={models} value={embedding} onChange={setEmbedding} allowDeselect={false} searchable
          description={t("It turns each passage into numbers for finding it by meaning. It can't be changed later; a small model is quicker.")} />
        <Textarea label={t("Instructions")} placeholder={t("Answer our shop's customers in a friendly tone.")} autosize minRows={2} maxLength={8000}
          value={instructions} onChange={(e) => setInstructions(e.currentTarget.value)} description={t("Optional: who the answers are for, and how to write them.")} />
        <Group justify="flex-end">
          <Button variant="default" onClick={close}>{t("Cancel")}</Button>
          <Button loading={create.isPending} disabled={!name.trim() || !embedding}
            onClick={() => create.mutate({ name: name.trim(), embeddingModelId: embedding!, languageModelId: answering ?? embedding!, instructions })}>{t("Make it")}</Button>
        </Group>
      </Stack>
    </Modal>
  );
}
