import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { api, enc } from "../../lib/api";
import { Async, Empty, Failed, Page, Panel } from "../../components/ui";
import { Confirm, Dialog } from "../../components/Dialog";
import type { Release } from "../../lib/types";
import {
  btn,
  btnSmall,
  btnSmallLink,
  formatBytes,
  timeAgo,
  useRefs,
  useRepoScope,
} from "./shared";

/** ReleasesTab is the design's releases screen: every published tag, its
 * notes, and the files handed out with it. A release can only be published
 * on a tag the repository already has, so the tag is chosen from the
 * repository's own tags rather than typed. */
export function ReleasesTab() {
  const { org, repo } = useRepoScope();
  const base = `/api/v1/orgs/${enc(org)}/repos/${enc(repo)}`;
  const qc = useQueryClient();
  const [publishing, setPublishing] = useState(false);
  const [deleting, setDeleting] = useState<string | null>(null);
  const [uploadError, setUploadError] = useState<unknown>(null);

  const { tags } = useRefs(org, repo);
  const tagNames = (tags.data?.refs ?? []).map((t) => t.name);

  const releases = useQuery({
    queryKey: ["releases", org, repo],
    queryFn: () => api.get<{ releases: Release[] }>(`${base}/releases`),
  });
  const invalidate = () =>
    void qc.invalidateQueries({ queryKey: ["releases", org, repo] });

  const publish = useMutation({
    mutationFn: (v: Record<string, string>) =>
      api.post<Release>(`${base}/releases`, {
        tag: v.tag,
        name: v.name,
        body: v.body,
      }),
    onSuccess: () => {
      setPublishing(false);
      invalidate();
    },
  });

  const remove = useMutation({
    mutationFn: (tag: string) => api.del(`${base}/releases/${enc(tag)}`),
    onSuccess: () => {
      setDeleting(null);
      invalidate();
    },
  });

  const addAsset = useMutation({
    mutationFn: ({ tag, file }: { tag: string; file: File }) =>
      api.upload(
        `${base}/releases/${enc(tag)}/assets?name=${enc(file.name)}`,
        file,
      ),
    onSuccess: () => {
      setUploadError(null);
      invalidate();
    },
    onError: setUploadError,
  });

  return (
    <Page
      title="Releases"
      subtitle={
        tagNames.length === 0
          ? "No tags yet — a release is published on a tag."
          : `${(releases.data?.releases ?? []).length} releases on ${tagNames.length} tags`
      }
      actions={
        <button
          style={btn}
          onClick={() => {
            publish.reset();
            setPublishing(true);
          }}
          disabled={tagNames.length === 0}
          title={
            tagNames.length === 0
              ? "Push a tag first: a release is published on a tag."
              : undefined
          }
        >
          Publish a release
        </button>
      }
    >
      {publishing ? (
        <Dialog
          title="Publish a release"
          submitLabel="Publish"
          fields={[
            {
              name: "tag",
              label: "Tag",
              required: true,
              type: "select",
              options: tagNames,
              help: "Only a tag that exists in this repository can carry a release.",
            },
            { name: "name", label: "Title", placeholder: "v1.0.0" },
            { name: "body", label: "Notes", type: "textarea" },
          ]}
          busy={publish.isPending}
          error={publish.error}
          onSubmit={(v) => publish.mutate(v)}
          onClose={() => setPublishing(false)}
        />
      ) : null}

      {deleting !== null ? (
        <Confirm
          title={`Delete release ${deleting}`}
          body={
            <>
              This deletes the release and un-publishes its asset download
              links. The tag and its history stay in the repository.
            </>
          }
          confirmLabel="Delete release"
          danger
          busy={remove.isPending}
          error={remove.error}
          onConfirm={() => remove.mutate(deleting)}
          onClose={() => setDeleting(null)}
        />
      ) : null}

      {tags.error ? <Failed error={tags.error} /> : null}

      <Panel>
        <Async query={releases}>
          {(d) =>
            d.releases.length === 0 ? (
              <Empty>
                No releases yet. Publish one on a tag to hand out a build.
              </Empty>
            ) : (
              d.releases.map((rel, i) => {
                // The list is newest first; the release before this one in
                // the list is the thing it can be diffed against.
                const previous = d.releases[i + 1];
                return (
                  <div
                    key={rel.id}
                    style={{ borderBottom: "1px solid var(--line)" }}
                  >
                    <div
                      style={{
                        display: "flex",
                        alignItems: "center",
                        gap: 10,
                        flexWrap: "wrap",
                        padding: "10px 14px",
                        borderBottom: "1px solid var(--line)",
                      }}
                    >
                      <span style={{ font: "600 13px var(--mono)" }}>
                        {rel.tag}
                      </span>
                      <span
                        style={{
                          font: "12px var(--sans)",
                          color: "var(--fg-dim)",
                        }}
                      >
                        {rel.name}
                      </span>
                      {i === 0 ? (
                        <span
                          style={{
                            background: "var(--ok-bg)",
                            color: "var(--ok)",
                            borderRadius: 99,
                            padding: "1px 8px",
                            font: "600 10px var(--mono)",
                          }}
                        >
                          Latest
                        </span>
                      ) : null}
                      <div style={{ flex: 1 }} />
                      <span
                        style={{
                          font: "11px var(--mono)",
                          color: "var(--fg-faint)",
                        }}
                      >
                        {timeAgo(rel.created_at)}
                      </span>
                      {previous ? (
                        <Link
                          to={`/repos/${enc(repo)}/compare?base=${enc(previous.tag)}&head=${enc(rel.tag)}`}
                          style={btnSmallLink}
                        >
                          compare to {previous.tag}
                        </Link>
                      ) : null}
                      <label style={assetAction}>
                        add asset
                        <input
                          type="file"
                          style={{ display: "none" }}
                          disabled={addAsset.isPending}
                          onChange={(e) => {
                            const file = e.target.files?.[0];
                            // The input is cleared so choosing the same file twice
                            // still fires a change, which is how a retry after a
                            // failed upload silently did nothing.
                            e.target.value = "";
                            if (file) addAsset.mutate({ tag: rel.tag, file });
                          }}
                        />
                      </label>
                      <button
                        style={{ ...btnSmall, color: "var(--bad)" }}
                        onClick={() => setDeleting(rel.tag)}
                      >
                        delete
                      </button>
                    </div>

                    {rel.body ? (
                      <div
                        style={{
                          font: "12px/1.7 var(--sans)",
                          color: "var(--fg-muted)",
                          padding: "10px 14px",
                          whiteSpace: "pre-wrap",
                          borderBottom: "1px solid var(--line)",
                        }}
                      >
                        {rel.body}
                      </div>
                    ) : null}

                    {rel.assets.length === 0 ? (
                      <div
                        style={{
                          padding: "8px 14px",
                          font: "11px var(--sans)",
                          color: "var(--fg-faint)",
                        }}
                      >
                        No assets on this release.
                      </div>
                    ) : (
                      <div style={{ padding: "6px 0" }}>
                        {rel.assets.map((a) => (
                          <div
                            key={a.id}
                            style={{
                              display: "flex",
                              alignItems: "center",
                              gap: 10,
                              padding: "5px 14px",
                              font: "11px var(--mono)",
                              color: "var(--fg-muted)",
                            }}
                          >
                            {/* The credential lives in this page, not in a cookie,
                                so a plain link cannot fetch the file. */}
                            <button
                              onClick={() =>
                                void api.download(
                                  `${base}/releases/${enc(rel.tag)}/assets/${enc(a.name)}`,
                                  a.name,
                                )
                              }
                              style={{ ...assetAction, color: "var(--link)" }}
                            >
                              {a.name}
                            </button>
                            <span>{formatBytes(a.size_bytes)}</span>
                            <span style={{ color: "var(--fg-faint)" }}>
                              {a.content_type}
                            </span>
                          </div>
                        ))}
                      </div>
                    )}
                  </div>
                );
              })
            )
          }
        </Async>
        {uploadError ? <Failed error={uploadError} /> : null}
        {remove.error && deleting === null ? (
          <Failed error={remove.error} />
        ) : null}
      </Panel>
    </Page>
  );
}

const assetAction: React.CSSProperties = {
  background: "transparent",
  border: "none",
  padding: 0,
  color: "var(--fg-muted)",
  font: "10px var(--sans)",
  cursor: "pointer",
};
