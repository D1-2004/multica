// @vitest-environment jsdom

import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../locales/en/common.json";
import { AvatarUploadControl } from "./avatar-upload-control";

const TEST_RESOURCES = {
  en: { common: enCommon },
};

const mocks = vi.hoisted(() => ({
  upload: vi.fn(),
}));

vi.mock("@multica/core/api", () => ({
  api: { getBaseUrl: () => "https://app.example.com" },
}));

vi.mock("@multica/core/hooks/use-file-upload", () => ({
  useFileUpload: () => ({
    upload: mocks.upload,
    uploadWithToast: vi.fn(),
    uploading: false,
  }),
}));

// The real crop dialog draws on a canvas, which jsdom lacks. Stub it with a
// confirm button that hands the picked file straight back.
vi.mock("./avatar-crop-dialog", () => ({
  AvatarCropDialog: ({
    file,
    open,
    onCropped,
  }: {
    file: File | null;
    open: boolean;
    onCropped: (f: File) => void;
  }) =>
    open && file ? (
      <button
        type="button"
        data-testid="crop-confirm"
        onClick={() => onCropped(file)}
      >
        crop
      </button>
    ) : null,
}));

function renderControl(onUploaded: (url: string) => void) {
  return render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <AvatarUploadControl
        value={null}
        variant="agent"
        name="Test Agent"
        onUploaded={onUploaded}
      />
    </I18nProvider>,
  );
}

async function pickAndCrop(container: HTMLElement) {
  const input = container.querySelector<HTMLInputElement>(
    'input[type="file"]',
  );
  expect(input).not.toBeNull();
  const file = new File(["png-bytes"], "avatar.png", { type: "image/png" });
  fireEvent.change(input!, { target: { files: [file] } });
  fireEvent.click(await screen.findByTestId("crop-confirm"));
}

describe("AvatarUploadControl", () => {
  beforeEach(() => {
    mocks.upload.mockReset();
  });

  it("persists the durable markdown_url when the server provides one", async () => {
    // Private-storage deployment: the raw storage URL points at a VPC-only
    // endpoint browsers cannot reach; markdown_url is the server-proxied
    // durable URL and must be what gets persisted.
    const raw =
      "https://bucket.oss-internal.example.com/workspaces/ws-1/att-1.webp";
    const durable = "https://app.example.com/api/attachments/att-1/download";
    mocks.upload.mockResolvedValue({
      id: "att-1",
      url: raw,
      link: raw,
      markdown_url: durable,
      markdownLink: durable,
      filename: "avatar.png",
    });
    const onUploaded = vi.fn();

    const { container } = renderControl(onUploaded);
    await pickAndCrop(container);

    await waitFor(() => expect(onUploaded).toHaveBeenCalledWith(durable));
  });

  it("falls back to the raw upload URL when markdown_url is absent", async () => {
    // The no-workspace upload branch returns {id, url, filename} with no
    // attachment row, so there is no proxy endpoint to point at.
    const raw = "https://cdn.example.com/users/u-1/att-2.webp";
    mocks.upload.mockResolvedValue({
      id: "att-2",
      url: raw,
      link: raw,
      markdown_url: undefined,
      markdownLink: raw,
      filename: "avatar.png",
    });
    const onUploaded = vi.fn();

    const { container } = renderControl(onUploaded);
    await pickAndCrop(container);

    await waitFor(() => expect(onUploaded).toHaveBeenCalledWith(raw));
  });
});
