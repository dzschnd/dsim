"use client";

import { useState } from "react";

type CreateLinkResponse = {
  id: string;
  nodeAId: string;
  nodeBId: string;
  networkId: string;
  networkName: string;
  createdAt: string;
};

export function CreateLinkForm() {
  const [nodeAId, setNodeAId] = useState<string>("");
  const [nodeBId, setNodeBId] = useState<string>("");
  const [message, setMessage] = useState<string>("");

  const handle = async () => {
    const baseUrl = process.env.NEXT_PUBLIC_API_BASE_URL ?? "";
    const res = await fetch(`${baseUrl}/api/v1/links`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ nodeAId, nodeBId }),
    });
    const data: CreateLinkResponse = await res.json();
    setMessage(JSON.stringify(data) ?? "");
  };

  return (
    <div>
      <div className="flex gap-2">
        <input
          value={nodeAId}
          onChange={(e) => setNodeAId(e.target.value)}
          placeholder="nodeAId"
          className="border-2 border-white p-2"
        />
        <input
          value={nodeBId}
          onChange={(e) => setNodeBId(e.target.value)}
          placeholder="nodeBId"
          className="border-2 border-white p-2"
        />
        <button
          type="button"
          onClick={handle}
          className="cursor-pointer border-2 border-white p-2"
        >
          Create link
        </button>
      </div>
      {message && <div>Response: {message}</div>}
    </div>
  );
}
