import { useMutation } from "@connectrpc/connect-query";
import { Alert, Button, Card, Group, Modal, Select, SimpleGrid, Stack, Text, Textarea, TextInput } from "@mantine/core";
import { IconAlertCircle, IconFileText } from "@tabler/icons-react";
import { useQueryClient, useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useState } from "react";

import { message } from "../../../api/client";
import { DocumentService } from "../../../api/gen/mimir/v1/document_pb";
import { allDocumentQueries, listCollections, listModels, whoAmI } from "../../../api/queries";
import { AnchorLink } from "../../../components/links";
import { Page } from "../../../components/Page";
import { bytes } from "../../../lib/format";

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
      title="Ask your documents"
      intro="Put your handbooks, policies and notes in a collection, and ask questions about them in plain words. Answers come from your documents, with the passages they used, and never leave this server."
      actions={canWrite && <Button leftSection={<IconFileText size={18} />} disabled={language.length === 0} onClick={() => setAdding(true)}>New collection</Button>}
    >
      {data.collections.length === 0 ? (
        <Card withBorder radius="lg" padding="xl" className="text-center">
          <Text fw={600}>No collections yet</Text>
          <Text c="dimmed" mb="md">
            {language.length === 0
              ? "Add a language model first, such as a Llama, Qwen or Gemma file. It reads your documents and writes the answers."
              : canWrite ? "Make a collection, add your files, and ask away." : "Someone who can add models makes collections; you can ask them here."}
          </Text>
          {canWrite && language.length > 0 && <Group justify="center"><Button onClick={() => setAdding(true)}>New collection</Button></Group>}
        </Card>
      ) : (
        <SimpleGrid cols={{ base: 1, sm: 2 }}>
          {data.collections.map((c) => (
            <Card key={c.id} withBorder radius="lg" padding="md">
              <AnchorLink fw={700} to="/documents/$id" params={{ id: c.id }} c="inherit">{c.name}</AnchorLink>
              <Text size="sm" c="dimmed">
                {c.documents} {c.documents === 1 ? "document" : "documents"} · {bytes(c.bytes)} · answered by {c.languageModelName || "a deleted model"}
              </Text>
              {c.instructions && <Text size="sm" mt={4} lineClamp={2}>{c.instructions}</Text>}
            </Card>
          ))}
        </SimpleGrid>
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
    <Modal opened={open} onClose={close} title="New collection" radius="lg" size="lg" centered>
      <Stack gap="md">
        {create.error && <Alert color="red" icon={<IconAlertCircle />}>{message(create.error)}</Alert>}
        <TextInput label="Name" placeholder="Shop handbook" maxLength={100} value={name} onChange={(e) => setName(e.currentTarget.value)}
          description="Apps that speak the OpenAI API ask it by this name." data-autofocus />
        <Select label="Model that answers" data={models} value={answering ?? embedding} onChange={setAnswering} allowDeselect={false} searchable
          description="It writes the answers from the passages found. You can change it later." />
        <Select label="Model that reads the documents" data={models} value={embedding} onChange={setEmbedding} allowDeselect={false} searchable
          description="It turns each passage into numbers for finding it by meaning. It can't be changed later; a small model is quicker." />
        <Textarea label="Instructions" placeholder="Answer our shop's customers in a friendly tone." autosize minRows={2} maxLength={8000}
          value={instructions} onChange={(e) => setInstructions(e.currentTarget.value)} description="Optional: who the answers are for, and how to write them." />
        <Group justify="flex-end">
          <Button variant="default" onClick={close}>Cancel</Button>
          <Button loading={create.isPending} disabled={!name.trim() || !embedding}
            onClick={() => create.mutate({ name: name.trim(), embeddingModelId: embedding!, languageModelId: answering ?? embedding!, instructions })}>Make it</Button>
        </Group>
      </Stack>
    </Modal>
  );
}
