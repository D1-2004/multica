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
    <div className="flex min-h-0 min-w-0 w-full flex-col gap-6">
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
        <StableFCE2BRuntimeOverviewPage embedded />
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
  const [expandedProviders, setExpandedProviders] = useState<number[]>([]);
  const [providerSearch, setProviderSearch] = useState("");
  const [modelSearch, setModelSearch] = useState("");
  const [providerFilter, setProviderFilter] = useState("");
  const [selectionFilter, setSelectionFilter] = useState("all");
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
    .flatMap((p) =>
      p.models.filter(Boolean).map((model) => ({ provider: p.id, model })),
    );
  const selected = new Set(draft.agentModels.map(refKey));
  const visibleRefs = refs.filter(
    (ref) =>
      (!providerFilter || ref.provider === providerFilter) &&
      `${ref.provider}/${ref.model}`
        .toLowerCase()
        .includes(modelSearch.trim().toLowerCase()) &&
      (selectionFilter === "all" ||
        selected.has(refKey(ref)) === (selectionFilter === "selected")),
  );
  const choose = (value: string) => refs.find((r) => refKey(r) === value);
  return (
    <div className="flex w-full min-w-0 flex-col gap-6">
      <section className="flex flex-col gap-4">
        <h3 className="text-title font-semibold">
          {t(($) => $.developer.providers)}
        </h3>
        <Input
          aria-label={t(($) => $.developer.search_providers)}
          placeholder={t(($) => $.developer.search_providers)}
          value={providerSearch}
          onChange={(e) => setProviderSearch(e.target.value)}
        />
        {draft.providers.every(
          (p) =>
            !`${p.name} ${p.id}`
              .toLowerCase()
              .includes(providerSearch.trim().toLowerCase()),
        ) && (
          <p className="text-body text-muted-foreground">
            {t(($) => $.developer.no_matches)}
          </p>
        )}
        {draft.providers.map((p, i) => (
          <details
            key={i}
            open={expandedProviders.includes(i)}
            className={`rounded-lg border border-surface-border ${`${p.name} ${p.id}`.toLowerCase().includes(providerSearch.trim().toLowerCase()) ? "" : "hidden"}`}
          >
            <summary
              onClick={(e) => {
                e.preventDefault();
                setExpandedProviders((open) =>
                  open.includes(i)
                    ? open.filter((index) => index !== i)
                    : [...open, i],
                );
              }}
              className="cursor-pointer rounded-lg p-4 focus-visible:outline focus-visible:outline-2"
            >
              <strong className="ml-2">
                {p.name || p.id || t(($) => $.developer.new_provider)}
              </strong>
              <span className="ml-3 text-caption text-muted-foreground">
                {p.id} ·{" "}
                {t(($) => $.developer.model_count, {
                  count: p.models.filter(Boolean).length,
                })}{" "}
                ·{" "}
                {p.builtin
                  ? t(($) => $.developer.diamond)
                  : p.enabled
                    ? t(($) => $.developer.enabled)
                    : t(($) => $.developer.disabled)}
              </span>
            </summary>
            <div className="flex flex-col gap-3 border-t border-surface-border p-4">
              <div className="flex items-center justify-end">
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
              <ProviderCatalog
                models={p.models}
                readonly={p.builtin}
                onChange={(models) => update(i, { models })}
              />
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
                      setMessage(
                        t(($) => $.developer.probe_fail, { status: 0 }),
                      );
                    }
                  }}
                >
                  {t(($) => $.developer.probe)}
                </Button>
              </div>
            </div>
          </details>
        ))}
        <Button
          variant="outline"
          onClick={() => {
            setProviderSearch("");
            setExpandedProviders((open) => [...open, draft.providers.length]);
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
            }));
          }}
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
        <div className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_auto_auto]">
          <Input
            aria-label={t(($) => $.developer.search_agent_models)}
            placeholder={t(($) => $.developer.search_agent_models)}
            value={modelSearch}
            onChange={(e) => setModelSearch(e.target.value)}
          />
          <select
            className="min-w-0 rounded border bg-background p-2"
            aria-label={t(($) => $.developer.filter_provider)}
            value={providerFilter}
            onChange={(e) => setProviderFilter(e.target.value)}
          >
            <option value="">{t(($) => $.developer.all_providers)}</option>
            {draft.providers
              .filter((p) => p.enabled)
              .map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name || p.id} ({p.id})
                </option>
              ))}
          </select>
          <select
            className="min-w-0 rounded border bg-background p-2"
            aria-label={t(($) => $.developer.filter_selection)}
            value={selectionFilter}
            onChange={(e) => setSelectionFilter(e.target.value)}
          >
            <option value="all">{t(($) => $.developer.all_models)}</option>
            <option value="selected">
              {t(($) => $.developer.selected_models)}
            </option>
            <option value="unselected">
              {t(($) => $.developer.unselected_models)}
            </option>
          </select>
        </div>
        <p className="text-caption text-muted-foreground" role="status">
          {t(($) => $.developer.filter_count, {
            visible: visibleRefs.length,
            total: refs.length,
            selected: draft.agentModels.length,
          })}
        </p>
        <div className="grid max-h-72 gap-2 overflow-auto rounded-lg border border-surface-border p-3 md:grid-cols-2">
          {visibleRefs.length === 0 && (
            <p className="text-body text-muted-foreground">
              {t(($) => $.developer.no_matches)}
            </p>
          )}
          {visibleRefs.map((ref) => (
            <label
              key={refKey(ref)}
              className="flex min-w-0 items-start gap-2 break-all rounded p-1 text-body"
            >
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

function ProviderCatalog({
  models,
  readonly,
  onChange,
}: {
  models: string[];
  readonly: boolean;
  onChange: (models: string[]) => void;
}) {
  const { t } = useT("settings");
  const [query, setQuery] = useState("");
  const [editing, setEditing] = useState(false);
  const visible = models.filter(
    (model) =>
      model && model.toLowerCase().includes(query.trim().toLowerCase()),
  );
  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center justify-between gap-2">
        <span className="text-body font-medium">
          {t(($) => $.developer.catalog_title)} ·{" "}
          {models.filter(Boolean).length}
        </span>
        {!readonly && (
          <Button variant="outline" onClick={() => setEditing(!editing)}>
            {editing
              ? t(($) => $.developer.done_editing)
              : t(($) => $.developer.edit_catalog)}
          </Button>
        )}
      </div>
      {editing ? (
        <label>
          <span className="text-caption text-muted-foreground">
            {t(($) => $.developer.catalog)}
          </span>
          <Textarea
            style={{ fieldSizing: "fixed" }}
            className="h-48 min-h-0 max-h-72 resize-y overflow-y-auto field-sizing-fixed"
            value={models.join("\n")}
            onChange={(e) =>
              onChange(e.target.value.split("\n").map((s) => s.trim()))
            }
          />
        </label>
      ) : (
        <>
          <Input
            aria-label={t(($) => $.developer.search_catalog)}
            placeholder={t(($) => $.developer.search_catalog)}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
          />
          <div
            className="max-h-48 overflow-y-auto rounded border border-surface-border p-2"
            role="region"
            aria-label={t(($) => $.developer.catalog_title)}
            tabIndex={0}
          >
            {visible.length ? (
              visible.map((model, i) => (
                <div key={`${model}-${i}`} className="break-all py-1 text-body">
                  {model}
                </div>
              ))
            ) : (
              <p className="text-body text-muted-foreground">
                {t(($) => $.developer.no_matches)}
              </p>
            )}
          </div>
          <span className="text-caption text-muted-foreground">
            {t(($) => $.developer.catalog_count, {
              visible: visible.length,
              total: models.filter(Boolean).length,
            })}
          </span>
        </>
      )}
    </div>
  );
}
