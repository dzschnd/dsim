"use client";

import { useState } from "react";

type PingResponse = {
  message: string;
};

export function PingButton() {
  const [message, setMessage] = useState<string>("");

  const handlePing = async () => {
    const baseUrl = process.env.NEXT_PUBLIC_API_BASE_URL ?? "";
    const res = await fetch(`${baseUrl}`);
    const data: PingResponse = await res.json();
    setMessage(data.message ?? "");
  };

  return (
    <div>
      <button
        type="button"
        onClick={handlePing}
        className="cursor-pointer border-2 border-white p-2 "
      >
        Ping
      </button>
      {message && <div>Response: {message}</div>}
    </div>
  );
}
