import { CreateLinkForm } from "./_components/CreateLinkForm";
import { CreateButton } from "./_components/CreateButton";
import { DeleteLinkForm } from "./_components/DeleteLinkForm";
import { DeleteNodeForm } from "./_components/DeleteNodeForm";
import { ListLinksButton } from "./_components/ListLinksButton";
import { ListNodesButton } from "./_components/ListNodesButton";
import { PingButton } from "./_components/PingButton";

export default function Home() {
  return (
    <div>
      <PingButton />
      <CreateButton />
      <ListNodesButton />
      <DeleteNodeForm />
      <CreateLinkForm />
      <ListLinksButton />
      <DeleteLinkForm />
    </div>
  );
}
