export type ApiInterface = {
	id: string;
	name: string;
	linkId: string;
	ipAddress: string;
	prefixLength: number;
};

export type ApiNode = {
	id: string;
	position: TopologyPosition;
	status: string;
	type: string;
	createdAt: string;
	interfaces: ApiInterface[];
};

export type ApiLink = {
	id: string;
	interfaceAId: string;
	interfaceBId: string;
	networkId: string;
	networkName: string;
	createdAt: string;
};

type ApiError = {
	error: string;
};

export type ApiCommandResponse = {
	command: string;
	stdout: string;
	stderr: string;
	exitCode: number;
};

export type TopologyPosition = {
	x: number;
	y: number;
};

export type TopologyInterface = {
	name: string;
	cidr?: string;
};

export type TopologyRoute = {
	destination: string;
	gateway: string;
};

export type TopologyNode = {
	id: string;
	type: string;
	position: TopologyPosition;
	interfaces: TopologyInterface[];
	routes: TopologyRoute[];
	running: boolean;
};

export type TopologyLinkEndpoint = {
	nodeId: string;
	interface: string;
};

export type TopologyLink = {
	id: string;
	a: TopologyLinkEndpoint;
	b: TopologyLinkEndpoint;
};

export type TopologyFile = {
	nodes: TopologyNode[];
	links: TopologyLink[];
};

export async function parseApiError(res: Response): Promise<string> {
	const text = await res.text();
	if (!text) {
		return `${res.status} ${res.statusText}`;
	}

	try {
		const parsed = JSON.parse(text) as ApiError;
		if (parsed.error) {
			return `${res.status}: ${parsed.error}`;
		}
		return `${res.status} ${res.statusText}`;
	} catch {
		return `${res.status} ${res.statusText}`;
	}
}

async function parseCommandError(res: Response): Promise<string> {
	const text = await res.text();
	if (!text) {
		return res.statusText;
	}

	try {
		const parsed = JSON.parse(text) as ApiError;
		return parsed.error || res.statusText;
	} catch {
		return res.statusText;
	}
}

export async function fetchTopology(baseUrl: string): Promise<{
	nodes: ApiNode[];
	links: ApiLink[];
}> {
	const [nodesRes, linksRes] = await Promise.all([
		fetch(`${baseUrl}/api/v1/nodes`),
		fetch(`${baseUrl}/api/v1/links`),
	]);

	if (!nodesRes.ok) {
		throw new Error(await parseApiError(nodesRes));
	}
	if (!linksRes.ok) {
		throw new Error(await parseApiError(linksRes));
	}

	return {
		nodes: (await nodesRes.json()) as ApiNode[],
		links: (await linksRes.json()) as ApiLink[],
	};
}

export async function fetchNode(baseUrl: string, nodeID: string): Promise<ApiNode> {
	const res = await fetch(`${baseUrl}/api/v1/nodes/${nodeID}`);

	if (!res.ok) {
		throw new Error(await parseApiError(res));
	}

	return (await res.json()) as ApiNode;
}

export async function exportTopology(baseUrl: string): Promise<TopologyFile> {
	const res = await fetch(`${baseUrl}/api/v1/topology`);

	if (!res.ok) {
		throw new Error(await parseApiError(res));
	}

	return (await res.json()) as TopologyFile;
}

export async function importTopology(baseUrl: string, topology: TopologyFile): Promise<void> {
	const res = await fetch(`${baseUrl}/api/v1/topology`, {
		method: "POST",
		headers: { "Content-Type": "application/json" },
		body: JSON.stringify(topology),
	});

	if (!res.ok) {
		throw new Error(await parseApiError(res));
	}
}

export async function clearTopology(baseUrl: string): Promise<void> {
	const res = await fetch(`${baseUrl}/api/v1/topology`, {
		method: "DELETE",
	});

	if (!res.ok) {
		throw new Error(await parseApiError(res));
	}
}

export async function createNode(
	baseUrl: string,
	type: "host" | "switch" | "router",
	position: TopologyPosition,
): Promise<void> {
	const res = await fetch(`${baseUrl}/api/v1/nodes`, {
		method: "POST",
		headers: { "Content-Type": "application/json" },
		body: JSON.stringify({ type, position }),
	});

	if (!res.ok) {
		throw new Error(await parseApiError(res));
	}
}

export async function updateNodePosition(
	baseUrl: string,
	nodeID: string,
	position: TopologyPosition,
): Promise<void> {
	const res = await fetch(`${baseUrl}/api/v1/nodes/${nodeID}/position`, {
		method: "PATCH",
		headers: { "Content-Type": "application/json" },
		body: JSON.stringify(position),
	});

	if (!res.ok) {
		throw new Error(await parseApiError(res));
	}
}

export async function deleteNode(baseUrl: string, nodeID: string): Promise<void> {
	const res = await fetch(`${baseUrl}/api/v1/nodes/${nodeID}`, {
		method: "DELETE",
	});

	if (!res.ok) {
		throw new Error(await parseApiError(res));
	}
}

export async function startNode(baseUrl: string, nodeID: string): Promise<void> {
	const res = await fetch(`${baseUrl}/api/v1/nodes/${nodeID}/start`, {
		method: "POST",
	});

	if (!res.ok) {
		throw new Error(await parseApiError(res));
	}
}

export async function stopNode(baseUrl: string, nodeID: string): Promise<void> {
	const res = await fetch(`${baseUrl}/api/v1/nodes/${nodeID}/stop`, {
		method: "POST",
	});

	if (!res.ok) {
		throw new Error(await parseApiError(res));
	}
}

export async function createLink(
	baseUrl: string,
	interfaceAID: string,
	interfaceBID: string,
): Promise<ApiLink> {
	const res = await fetch(`${baseUrl}/api/v1/links`, {
		method: "POST",
		headers: { "Content-Type": "application/json" },
		body: JSON.stringify({ interfaceAId: interfaceAID, interfaceBId: interfaceBID }),
	});

	if (!res.ok) {
		throw new Error(await parseApiError(res));
	}

	return (await res.json()) as ApiLink;
}

export async function deleteLink(baseUrl: string, linkID: string): Promise<void> {
	const res = await fetch(`${baseUrl}/api/v1/links/${linkID}`, {
		method: "DELETE",
	});

	if (!res.ok) {
		throw new Error(await parseApiError(res));
	}
}

export async function runNodeCommand(
	baseUrl: string,
	nodeID: string,
	command: string,
): Promise<ApiCommandResponse> {
	const res = await fetch(`${baseUrl}/api/v1/nodes/${nodeID}/cli`, {
		method: "POST",
		headers: { "Content-Type": "application/json" },
		body: JSON.stringify({ command }),
	});

	if (!res.ok) {
		throw new Error(await parseCommandError(res));
	}

	return (await res.json()) as ApiCommandResponse;
}
