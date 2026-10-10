// saveBlob hands a fetched blob to the browser as a file download (object URL
// + a transient download link), the one way every authenticated download
// (bearer-fetched, so a plain link cannot carry the credential) is saved.
export function saveBlob(blob: Blob, filename: string): void {
  const objectUrl = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = objectUrl;
  link.download = filename;
  link.click();
  window.setTimeout(() => URL.revokeObjectURL(objectUrl), 0);
}
