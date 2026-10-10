import { Page } from "../components/ui";
import { Empty } from "../components/ui";

/** Rendered from the redesign's artboards; the screen is being wired to its
 * endpoints. An empty state is the honest rendering until then. */
export function Runners() {
  return (
    <Page title="Runners">
      <Empty>Coming up in this redesign.</Empty>
    </Page>
  );
}
