import { Group, Text, Title, UnstyledButton } from "@mantine/core";
import { IconBolt, IconDeviceMobile, IconLanguage, IconLock } from "@tabler/icons-react";
import type { ReactNode } from "react";
import { lang, languages, setLang, t } from "../i18n";

// The sign-in and setup screens: mimir's story on the left, the form on
// the right. On a phone the story shrinks to a band above the form.
export function AuthCard({ title, intro, wide, children }: { title: string; intro?: string; wide?: boolean; children: ReactNode }) {
  return (
    <div className="grid min-h-dvh lg:grid-cols-[minmax(0,1.05fr)_minmax(0,1fr)]">
      <BrandPanel />
      <main className="flex items-start justify-center px-4 pb-10 pt-8 sm:px-8 lg:items-center lg:py-12">
        <div className={`auth-in w-full ${wide ? "max-w-xl" : "max-w-sm"}`}>
          <Title order={1} fz={{ base: 26, sm: 30 }}>{title}</Title>
          {intro && <Text c="dimmed" mt={6}>{intro}</Text>}
          <div className="mt-7">{children}</div>
          <Languages />
        </div>
      </main>
    </div>
  );
}

// Languages lets people pick theirs before they sign in.
function Languages() {
  return (
    <Group gap="xs" mt="xl" c="dimmed" aria-label={t("Language")} role="group">
      <IconLanguage size={16} aria-hidden />
      {languages.map((l, i) => (
        <Group key={l.value} gap="xs">
          {i > 0 && <Text size="sm" aria-hidden>·</Text>}
          <UnstyledButton onClick={() => setLang(l.value)} lang={l.value} aria-pressed={lang === l.value}
            className="text-sm" fw={lang === l.value ? 700 : 400} c={lang === l.value ? "var(--mantine-color-text)" : "dimmed"}>{l.label}</UnstyledButton>
        </Group>
      ))}
    </Group>
  );
}

const points = [
  { icon: IconBolt, text: t("Teach a model from a spreadsheet, photos or text, without code.") },
  { icon: IconLock, text: t("Runs on your own server, next to your own data.") },
  { icon: IconDeviceMobile, text: t("Apps ask it questions; phones can answer offline.") },
];

// Network of nodes, drawn once; lines flow and nodes pulse in CSS.
const nodes: [number, number][] = [[60, 80], [180, 40], [300, 110], [120, 190], [250, 230], [380, 60], [400, 200], [330, 320], [160, 300], [40, 260]];
const edges: [number, number][] = [[0, 1], [1, 2], [0, 3], [3, 4], [2, 4], [1, 5], [5, 6], [2, 6], [4, 7], [6, 7], [3, 8], [8, 7], [8, 9], [9, 3]];

function BrandPanel() {
  return (
    <aside className="brand-panel flex flex-col justify-between px-6 py-6 sm:px-10 lg:min-h-dvh lg:px-14 lg:py-12" aria-label={t("About mimir")}>
      <div className="brand-orb a" aria-hidden />
      <div className="brand-orb b" aria-hidden />
      <div className="brand-orb c" aria-hidden />
      <svg className="brand-net pointer-events-none absolute right-[-150px] top-[6%] hidden h-[360px] w-[440px] opacity-60 lg:block" viewBox="0 0 440 360" aria-hidden>
        {edges.map(([a, b], i) => <line key={i} x1={nodes[a]![0]} y1={nodes[a]![1]} x2={nodes[b]![0]} y2={nodes[b]![1]} style={{ animationDelay: `${i * 0.2}s` }} />)}
        {nodes.map(([x, y], i) => <circle key={i} cx={x} cy={y} r={4} style={{ animationDelay: `${(i * 0.37) % 3}s` }} />)}
      </svg>

      <div className="flex items-center gap-3">
        <img src="/icon.svg" alt="" width={36} height={36} className="rounded-[10px] ring-1 ring-white/30" />
        <Text fw={750} ff="var(--font-display)" fz={24} c="white">mimir</Text>
        <Text size="xs" fw={600} className="rounded-full bg-white/15 px-2 py-0.5 text-white/90">{t("server")}</Text>
      </div>

      <div className="relative mt-6 max-w-lg lg:mt-0">
        <Text component="h2" ff="var(--font-display)" fw={700} c="white" className="brand-line text-[26px] leading-tight sm:text-[32px] lg:text-[44px]" style={{ letterSpacing: "-0.02em" }}>{t("Your own models,")}{" "}
          <span className="sr-only">{t("answering questions.")}</span>
          <span className="brand-rotate text-[#A0E3CF]" aria-hidden>
            <span>{t("answering questions.")}</span>
            <span>{t("spotting what's wrong.")}</span>
            <span>{t("sorting your photos.")}</span>
            <span>{t("learning every day.")}</span>
          </span>
        </Text>
        <Text c="rgba(255,255,255,.78)" mt="md" className="brand-line hidden sm:block" style={{ animationDelay: ".12s" }} fz="lg">{t("mimir-server runs machine learning models on your own machine. Teach them, try them, and let your apps ask them questions.")}</Text>
        <ul className="mt-8 hidden flex-col gap-4 lg:flex">
          {points.map((p, i) => (
            <li key={p.text} className="brand-line flex items-start gap-3" style={{ animationDelay: `${0.25 + i * 0.12}s` }}>
              <span className="grid h-9 w-9 flex-none place-items-center rounded-lg bg-white/12 ring-1 ring-white/20"><p.icon size={18} color="white" /></span>
              <Text c="rgba(255,255,255,.88)" pt={6}>{p.text}</Text>
            </li>
          ))}
        </ul>
      </div>

      <Text size="xs" c="rgba(255,255,255,.6)" className="hidden lg:block">{t("Runs anywhere Go runs. No cloud account needed.")}</Text>
    </aside>
  );
}
