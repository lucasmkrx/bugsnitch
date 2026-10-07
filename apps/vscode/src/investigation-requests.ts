// SPDX-License-Identifier: MPL-2.0
/** Latest invocation wins, including discovery that completes out of order. */
export class InvestigationRequests {
  private sequence = 0;
  private disposed = false;
  private documents = new Map<string, AbortController>();
  private repositories = new Map<
    string,
    { id: number; controller: AbortController }
  >();
  begin(document: string) {
    this.documents.get(document)?.abort();
    const controller = new AbortController();
    const id = ++this.sequence;
    this.documents.set(document, controller);
    if (this.disposed) controller.abort();
    return {
      signal: controller.signal,
      cancel: () => controller.abort(),
      claim: (root: string) => {
        const previous = this.repositories.get(root);
        if (controller.signal.aborted || (previous && previous.id > id))
          return false;
        previous?.controller.abort();
        this.repositories.set(root, { id, controller });
        return true;
      },
      current: (root: string) =>
        !controller.signal.aborted && this.repositories.get(root)?.id === id,
      finish: () => {
        if (this.documents.get(document) === controller)
          this.documents.delete(document);
      },
    };
  }
  dispose(): void {
    this.disposed = true;
    for (const controller of this.documents.values()) controller.abort();
    for (const { controller } of this.repositories.values()) controller.abort();
    this.documents.clear();
    this.repositories.clear();
  }
}
