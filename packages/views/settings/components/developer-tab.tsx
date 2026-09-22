"use client";
import { useState } from "react";
import {
  useTestProviderModel,
  useRestoreGlobalModels,
  useDeveloperCapabilities,
  useGlobalModels,
  useSaveGlobalModels,
  useDiscoverProviderModels,
  type GlobalModels,
  type ModelRef,
} from "@multica/core/global-models";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { StableFCE2BRuntimeOverviewPage } from "../../runtimes/components/stable-fc-e2b-runtime-overview-page";
import { useT } from "../../i18n";
const refKey = (v: ModelRef) => JSON.stringify([v.provider, v.model]);
export function DeveloperTab() {
  const { t } = useT("settings");
  const caps = useDeveloperCapabilities();
  const q = useGlobalModels(caps.data?.developer === true);
  const [section, setSection] = useState("models");
  if (caps.data?.developer !== true)
    return <p className="p-6">{t(($) => $.developer.no_access)}</p>;
  return (
    <div className="flex min-h-0 flex-col gap-6 p-6">
      <div>
        <h2 className="text-title font-semibold">
          {t(($) => $.developer.title)}
        </h2>
        <p className="text-body text-muted-foreground">
          {t(($) => $.developer.scope)}
        </p>
      </div>
      <div className="flex gap-2">
        <Button
          variant={section === "models" ? "default" : "outline"}
          onClick={() => setSection("models")}
        >
          {t(($) => $.developer.models)}
        </Button>
        <Button
          variant={section === "runtime" ? "default" : "outline"}
          onClick={() => setSection("runtime")}
        >
          {t(($) => $.developer.runtime)}
        </Button>
      </div>
      {section === "runtime" ? (
        <StableFCE2BRuntimeOverviewPage />
      ) : q.isError ? (
        <p role="alert">{t(($) => $.developer.load_failed)}</p>
      ) : q.data && q.data.revision >= 0 ? (
        <ModelEditor key={q.data.revision} initial={q.data} />
      ) : (
        <p>{t(($) => $.developer.loading)}</p>
      )}
    </div>
  );
}
function ModelEditor({ initial }: { initial: GlobalModels }) {
  const { t } = useT("settings");
  const [draft, setDraft] = useState(initial);
  const probe = useTestProviderModel();
  const [probeModels, setProbeModels] = useState<Record<number, string>>({});
  const save = useSaveGlobalModels();
  const restore = useRestoreGlobalModels();
  const discover = useDiscoverProviderModels();
  const [message, setMessage] = useState("");
  const update = (
    i: number,
    patch: Partial<GlobalModels["providers"][number]>,
  ) =>
    setDraft((c) => ({
      ...c,
      providers: c.providers.map((p, j) => (i === j ? { ...p, ...patch } : p)),
    }));
  const refs = draft.providers
    .filter((p) => p.enabled)
    .flatMap((p) => p.models.filter(Boolean).map((model) => ({ provider: p.id, model })));
  const choose = (value: string) => refs.find((r) => refKey(r) === value);
  return (
    <div className="flex max-w-5xl flex-col gap-6">
      <section className="flex flex-col gap-4">
        <h3 className="text-title font-semibold">
          {t(($) => $.developer.providers)}
        </h3>
        {draft.providers.map((p, i) => (
          <div
            key={i}
            className="flex flex-col gap-3 rounded-lg border border-surface-border p-4"
          >
            <div className="flex items-center justify-between">
              <strong>
                {p.name || p.id || t(($) => $.developer.new_provider)}
              </strong>
              {p.builtin ? (
                <span className="text-caption">
                  {t(($) => $.developer.diamond)}
                </span>
              ) : (
                <Button
                  variant="outline"
                  onClick={() =>
                    setDraft((c) => ({
                      ...c,
                      providers: c.providers.filter((_, j) => i !== j),
                    }))
                  }
                >
                  {t(($) => $.developer.remove)}
                </Button>
              )}
            </div>
            <div className="grid gap-3 md:grid-cols-2">
              <label>
                {t(($) => $.developer.identifier)}
                <Input
                  value={p.id}
                  disabled={
                    p.builtin || initial.providers.some((x) => x.id === p.id)
                  }
                  onChange={(e) => update(i, { id: e.target.value })}
                />
              </label>
              <label>
                {t(($) => $.developer.name)}
                <Input
                  value={p.name}
                  disabled={p.builtin}
                  onChange={(e) => update(i, { name: e.target.value })}
                />
              </label>
            </div>
            <label>
              {t(($) => $.developer.base_url)}
              <Input
                value={p.baseUrl}
                disabled={p.builtin}
                placeholder="https://provider.example/v1"
                onChange={(e) => update(i, { baseUrl: e.target.value })}
              />
            </label>
            {!p.builtin && (
              <label>
                {t(($) => $.developer.api_key)}
                <Input
                  type="password"
                  autoComplete="new-password"
                  value={p.apiKey}
                  placeholder={
                    p.hasKey
                      ? t(($) => $.developer.key_saved)
                      : t(($) => $.developer.key_required)
                  }
                  onChange={(e) => update(i, { apiKey: e.target.value })}
                />
              </label>
            )}
            <label>
              {t(($) => $.developer.catalog)}
              <Textarea
                value={p.models.join("\n")}
                disabled={p.builtin}
                onChange={(e) =>
                  update(i, {
                    models: e.target.value
                      .split("\n")
                      .map((s) => s.trim()),
                  })
                }
              />
            </label>
            {!p.builtin && (
              <div className="flex items-center gap-4">
                <label>
                  <input
                    type="checkbox"
                    checked={p.enabled}
                    onChange={(e) => update(i, { enabled: e.target.checked })}
                  />{" "}
                  {t(($) => $.developer.enabled)}
                </label>
                <Button
                  variant="outline"
                  disabled={discover.isPending}
                  onClick={async () => {
                    setMessage("");
                    try {
                      const result = await discover.mutateAsync(p);
                      if (!result.models.length) {
                        setMessage(t(($) => $.developer.empty_catalog));
                        return;
                      }
                      update(i, {
                        models: Array.from(
                          new Set([...p.models, ...result.models]),
                        ),
                      });
                    } catch {
                      setMessage(t(($) => $.developer.discovery_failed));
                    }
                  }}
                >
                  {t(($) => $.developer.discover)}
                </Button>
              </div>
            )}
            <div className="flex flex-wrap items-center gap-2">
              <select
                aria-label={t(($) => $.developer.probe_model)}
                className="min-w-0 flex-1 rounded border bg-background p-2"
                value={probeModels[i] ?? p.models[0] ?? ""}
                onChange={(e) =>
                  setProbeModels((v) => ({ ...v, [i]: e.target.value }))
                }
              >
                {p.models.map((m) => (
                  <option key={m} value={m}>
                    {m}
                  </option>
                ))}
              </select>
              <Button
                variant="outline"
                disabled={probe.isPending || p.models.length === 0}
                onClick={async () => {
                  try {
                    const r = await probe.mutateAsync({
                      provider: p,
                      model: probeModels[i] ?? p.models[0] ?? "",
                    });
                    setMessage(
                      r.valid
                        ? t(($) => $.developer.probe_pass)
                        : t(($) => $.developer.probe_fail, {
                            status: r.status,
                          }),
                    );
                  } catch {
                    setMessage(t(($) => $.developer.probe_fail, { status: 0 }));
                  }
                }}
              >
                {t(($) => $.developer.probe)}
              </Button>
            </div>
          </div>
        ))}
        <Button
          variant="outline"
          onClick={() =>
            setDraft((c) => ({
              ...c,
              providers: [
                ...c.providers,
                {
                  id: "",
                  name: "",
                  baseUrl: "",
                  apiKey: "",
                  models: [],
                  enabled: true,
                  builtin: false,
                  hasKey: false,
                },
              ],
            }))
          }
        >
          {t(($) => $.developer.add_provider)}
        </Button>
      </section>
      <section className="flex flex-col gap-3">
        <h3 className="text-title font-semibold">
          {t(($) => $.developer.agent_models)}
        </h3>
        <p className="text-body text-muted-foreground">
          {t(($) => $.developer.agent_help)}
        </p>
        <div className="grid max-h-72 gap-2 overflow-auto md:grid-cols-2">
          {refs.map((ref) => (
            <label key={refKey(ref)}>
              <input
                type="checkbox"
                checked={draft.agentModels.some(
                  (m) => refKey(m) === refKey(ref),
                )}
                onChange={(e) =>
                  setDraft((c) => ({
                    ...c,
                    agentModels: e.target.checked
                      ? [...c.agentModels, ref]
                      : c.agentModels.filter((m) => refKey(m) !== refKey(ref)),
                  }))
                }
              />{" "}
              {ref.provider}/{ref.model}
            </label>
          ))}
        </div>
        <label>
          {t(($) => $.developer.default_model)}
          <select
            className="block w-full rounded border bg-background p-2"
            value={refKey(draft.defaultModel)}
            onChange={(e) => {
              const ref = choose(e.target.value);
              if (ref) setDraft((c) => ({ ...c, defaultModel: ref }));
            }}
          >
            {draft.agentModels.map((ref) => (
              <option key={refKey(ref)} value={refKey(ref)}>
                {ref.provider}/{ref.model}
              </option>
            ))}
          </select>
        </label>
      </section>
      <section className="flex flex-col gap-3">
        <h3 className="text-title font-semibold">
          {t(($) => $.developer.coordinator)}
        </h3>
        <p className="text-body text-muted-foreground">
          {t(($) => $.developer.chain_help)}
        </p>
        {draft.coordinator.map((ref, i) => (
          <div key={i} className="flex items-center gap-2">
            <span>{i + 1}</span>
            <select
              className="min-w-0 flex-1 rounded border bg-background p-2"
              value={refKey(ref)}
              onChange={(e) => {
                const next = choose(e.target.value);
                if (next)
                  setDraft((c) => ({
                    ...c,
                    coordinator: c.coordinator.map((r, j) =>
                      i === j ? next : r,
                    ),
                  }));
              }}
            >
              {refs.map((m) => (
                <option key={refKey(m)} value={refKey(m)}>
                  {m.provider}/{m.model}
                </option>
              ))}
            </select>
            <Button
              variant="outline"
              onClick={() =>
                setDraft((c) => ({
                  ...c,
                  coordinator: c.coordinator.filter((_, j) => j !== i),
                }))
              }
            >
              {t(($) => $.developer.remove)}
            </Button>
          </div>
        ))}
        <Button
          variant="outline"
          disabled={draft.coordinator.length >= 5}
          onClick={() => {
            const next = refs.find(
              (r) => !draft.coordinator.some((m) => refKey(m) === refKey(r)),
            );
            if (next)
              setDraft((c) => ({
                ...c,
                coordinator: [...c.coordinator, next],
              }));
          }}
        >
          {t(($) => $.developer.add_fallback)}
        </Button>
        <label>
          <input
            type="checkbox"
            checked={draft.diamondFallback}
            onChange={(e) =>
              setDraft((c) => ({ ...c, diamondFallback: e.target.checked }))
            }
          />{" "}
          {t(($) => $.developer.last_diamond)}
        </label>
      </section>
      {restore.isError && <p role="alert">{restore.error.message}</p>}
      {message && <p role="status">{message}</p>}
      {save.isError && <p role="alert">{save.error.message}</p>}
      <div className="flex items-center gap-4">
        <Button
          disabled={save.isPending}
          onClick={async () => {
            setMessage("");
            await save.mutateAsync(draft).catch(() => {});
          }}
        >
          {t(($) => $.developer.save)}
        </Button>
        <Button
          variant="outline"
          disabled={initial.revision < 2 || restore.isPending}
          onClick={() => restore.mutate(initial.revision)}
        >
          {t(($) => $.developer.restore)}
        </Button>
        <span className="text-caption text-muted-foreground">
          {t(($) => $.developer.revision, { revision: initial.revision })}
        </span>
      </div>
    </div>
  );
}
