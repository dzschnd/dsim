"use client";

import { useState } from "react";

export function DeleteLinkForm() {
  const [linkId, setLinkId] = useState<string>("");
  const [message, setMessage] = useState<string>("");

  const handle = async () => {
    const baseUrl = process.env.NEXT_PUBLIC_API_BASE_URL ?? "";
    const res = await fetch(`${baseUrl}/api/v1/links/${linkId}`, {
      method: "DELETE",
    });
    setMessage(`Status: ${res.status}`);
  };

  return (
    <div>
      <div className="flex gap-2">
        <input
          value={linkId}
          onChange={(e) => setLinkId(e.target.value)}
          placeholder="linkId"
          className="border-2 border-white p-2"
        />
        <button
          type="button"
          onClick={handle}
          className="cursor-pointer border-2 border-white p-2"
        >
          Delete link
        </button>
      </div>
      {message && <div>Response: {message}</div>}
    </div>
  );
}
