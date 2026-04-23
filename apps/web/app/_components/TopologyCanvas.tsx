"use client";

import { useCallback, useEffect, useMemo, useRef, useState, type ChangeEvent } from "react";
import ReactFlow, {
	Background,
	Connection,
	ConnectionLineType,
	Controls,
	Edge,
	EdgeMouseHandler,
	MiniMap,
	Node,
	OnConnect,
	OnEdgesChange,
	OnNodesChange,
	applyEdgeChanges,
	applyNodeChanges,
} from "reactflow";
import "reactflow/dist/style.css";

import { InterfaceLabelEdge, type InterfaceLabelEdgeData } from "./InterfaceLabelEdge";
import { SquareNode, type SquareNodeData } from "./SquareNode";
import {
	type ApiCommandResponse,
	type ApiInterface,
	type TopologyFile,
	clearTopology,
	createNode as createNodeRequest,
	createLink,
	deleteLink,
	deleteNode,
	exportTopology,
	fetchTopology,
	importTopology,
	runNodeCommand,
	startNode,
	stopNode,
	updateNodePosition,
} from "../services/topology";

type TerminalState = {
	isOpen: boolean;
	input: string;
	lines: string[];
};

type PendingConnection = {
	sourceNodeID: string;
	targetNodeID: string;
	sourceInterfaceID: string;
	targetInterfaceID: string;
};

const EDGE_STYLE = {
	stroke: "#334155",
	strokeWidth: 3,
};

const SELECTED_EDGE_STYLE = {
	stroke: "#2563eb",
	strokeWidth: 4,
};

function randomPos(index: number) {
	const row = Math.floor(index / 5);
	const col = index % 5;
	return {
		x: 80 + col * 220,
		y: 100 + row * 180,
	};
}

function applySelectedNode(
	nodes: Node<SquareNodeData>[],
	selectedNodeId: string,
): Node<SquareNodeData>[] {
	return nodes.map((node) => ({
		...node,
		selected: node.id === selectedNodeId,
		data: {
			...node.data,
			isSelected: node.id === selectedNodeId,
		},
	}));
}

function applySelectedEdge(edges: Edge[], selectedLinkId: string): Edge[] {
	return edges.map((edge) => ({
		...edge,
		selected: edge.id === selectedLinkId,
		style: edge.id === selectedLinkId ? SELECTED_EDGE_STYLE : EDGE_STYLE,
	}));
}

function getAvailableInterfaces(interfaces: ApiInterface[]): ApiInterface[] {
	return interfaces.filter((iface) => iface.linkId === "");
}

function findNodeIDByInterfaceID(nodes: Node<SquareNodeData>[], interfaceID: string): string {
	const node = nodes.find((candidate) =>
		candidate.data.interfaces.some((iface) => iface.id === interfaceID),
	);
	return node?.id ?? "";
}

function findInterfaceNameByID(nodes: Node<SquareNodeData>[], interfaceID: string): string {
	for (const node of nodes) {
		const iface = node.data.interfaces.find((candidate) => candidate.id === interfaceID);
		if (iface) {
			return iface.name;
		}
	}
	return "";
}

const nodeTypes = {
	square: SquareNode,
};

const edgeTypes = {
	interfaceLabel: InterfaceLabelEdge,
};

export function TopologyCanvas() {
	const baseUrl = process.env.NEXT_PUBLIC_API_BASE_URL ?? "";

	const [nodes, setNodes] = useState<Node<SquareNodeData>[]>([]);
	const [edges, setEdges] = useState<Edge[]>([]);
	const [selectedNodeId, setSelectedNodeId] = useState<string>("");
	const [selectedLinkId, setSelectedLinkId] = useState<string>("");
	const [pendingConnection, setPendingConnection] = useState<PendingConnection | null>(null);
	const [connectionSourceNodeId, setConnectionSourceNodeId] = useState<string>("");
	const [isCreateNodeMenuOpen, setIsCreateNodeMenuOpen] = useState<boolean>(false);
	const [fullscreenTerminalNodeId, setFullscreenTerminalNodeId] = useState<string>("");
	const [busyNodeIds, setBusyNodeIds] = useState<Set<string>>(new Set());
	const [busy, setBusy] = useState<boolean>(false);
	const [status, setStatus] = useState<string>("");
	const selectedNodeIdRef = useRef<string>("");
	const selectedLinkIdRef = useRef<string>("");
	const importInputRef = useRef<HTMLInputElement | null>(null);
	const connectionSourceNodeIdRef = useRef<string>("");
	const fullscreenTerminalNodeIdRef = useRef<string>("");
	const pendingConnectionRef = useRef<PendingConnection | null>(null);
	const nodePositionsRef = useRef<Map<string, { x: number; y: number }>>(
		new Map(),
	);
	const nodesRef = useRef<Node<SquareNodeData>[]>([]);
	const terminalStateRef = useRef<Map<string, TerminalState>>(new Map());
	const toggleNodeRunRef = useRef<(nodeID: string) => Promise<void>>(async () => { });
	const toggleTerminalRef = useRef<(nodeID: string) => void>(() => { });
	const toggleTerminalFullscreenRef = useRef<(nodeID: string) => void>(() => { });
	const updateTerminalInputRef = useRef<(nodeID: string, value: string) => void>(() => { });
	const submitTerminalInputRef = useRef<(nodeID: string) => Promise<void>>(async () => { });

	const edgeByPair = useMemo(() => {
		const map = new Map<string, Edge>();
		for (const edge of edges) {
			const a = String(edge.source);
			const b = String(edge.target);
			const key = a < b ? `${a}|${b}` : `${b}|${a}`;
			map.set(key, edge);
		}
		return map;
	}, [edges]);

	const loadTopology = useCallback(async () => {
		setBusy(true);
		setStatus("Loading topology...");
		try {
			const { nodes: apiNodes, links: apiLinks } = await fetchTopology(baseUrl);
			const existingPositions = nodePositionsRef.current;
			const currentSelectedNodeId = selectedNodeIdRef.current;
			const terminalState = terminalStateRef.current;

			const flowNodes: Node<SquareNodeData>[] = apiNodes.map((node, index) => ({
				id: node.id,
				type: "square",
				position: existingPositions.get(node.id) ?? node.position ?? randomPos(index),
				zIndex: 10,
				selected: node.id === currentSelectedNodeId,
				data: {
					nodeId: node.id,
					status: node.status,
					type: node.type,
					interfaces: node.interfaces,
					isSelected: node.id === currentSelectedNodeId,
					isBusy: busyNodeIds.has(node.id),
					connectionSourceNodeId: connectionSourceNodeIdRef.current,
					isTerminalOpen:
						node.status === "running" ? (terminalState.get(node.id)?.isOpen ?? false) : false,
					isTerminalFullscreen: fullscreenTerminalNodeIdRef.current === node.id,
					terminalInput: terminalState.get(node.id)?.input ?? "",
					terminalLines: terminalState.get(node.id)?.lines ?? [],
					onToggleRun: () => void toggleNodeRunRef.current(node.id),
					onToggleTerminal: () => toggleTerminalRef.current(node.id),
					onToggleTerminalFullscreen: () => toggleTerminalFullscreenRef.current(node.id),
					onTerminalInputChange: (value: string) => updateTerminalInputRef.current(node.id, value),
					onTerminalSubmit: () => submitTerminalInputRef.current(node.id),
				},
			}));

			const flowEdges: Edge<InterfaceLabelEdgeData>[] = apiLinks.map((link) => ({
				id: link.id,
				type: "interfaceLabel",
				source: findNodeIDByInterfaceID(flowNodes, link.interfaceAId),
				target: findNodeIDByInterfaceID(flowNodes, link.interfaceBId),
				style: link.id === selectedLinkIdRef.current ? SELECTED_EDGE_STYLE : EDGE_STYLE,
				data: {
					interfaceAName: findInterfaceNameByID(flowNodes, link.interfaceAId),
					interfaceBName: findInterfaceNameByID(flowNodes, link.interfaceBId),
				},
			}));

			setNodes(applySelectedNode(flowNodes, currentSelectedNodeId));
			setEdges(applySelectedEdge(flowEdges, selectedLinkIdRef.current));
			setStatus(`Loaded ${flowNodes.length} nodes, ${flowEdges.length} links`);
		} catch (err: unknown) {
			const message = err instanceof Error ? err.message : String(err);
			setStatus(message || "Failed to load topolgy");
		} finally {
			setBusy(false);
		}
	}, [baseUrl, busyNodeIds]);

	const setNodeBusy = useCallback((nodeID: string, nextBusy: boolean) => {
		setBusyNodeIds((current) => {
			const updated = new Set(current);
			if (nextBusy) {
				updated.add(nodeID);
			} else {
				updated.delete(nodeID);
			}
			return updated;
		});
	}, []);

	const toggleNodeRun = useCallback(
		async (nodeID: string) => {
			const currentNode = nodesRef.current.find((node) => node.id === nodeID);
			if (!currentNode) {
				setStatus("Node not found in canvas");
				return;
			}

			const action = currentNode.data.status === "running" ? "stop" : "start";

			setNodeBusy(nodeID, true);
			setStatus(`${action === "start" ? "Starting" : "Stopping"} node ${nodeID}...`);
			try {
				if (action === "start") {
					await startNode(baseUrl, nodeID);
				} else {
					await stopNode(baseUrl, nodeID);
				}
				try {
					await loadTopology();
					setStatus(`Node ${nodeID} ${action === "start" ? "started" : "stopped"}`);
				} catch (err: unknown) {
					const message = err instanceof Error ? err.message : String(err);
					setStatus(
						`Node ${nodeID} ${action === "start" ? "started" : "stopped"}, but topology reload failed: ${message}`,
					);
				}
			} catch (err: unknown) {
				const message = err instanceof Error ? err.message : String(err);
				setStatus(`Failed to ${action} node: ${message}`);
			} finally {
				setNodeBusy(nodeID, false);
			}
		},
		[baseUrl, loadTopology, setNodeBusy],
	);

	useEffect(() => {
		void loadTopology();
	}, [loadTopology]);

	useEffect(() => {
		nodePositionsRef.current = new Map(
			nodes.map((node) => [
				node.id,
				{
					x: node.position.x,
					y: node.position.y,
				},
			]),
		);
		nodesRef.current = nodes;
		terminalStateRef.current = new Map(
			nodes.map((node) => [
				node.id,
				{
					isOpen: node.data.isTerminalOpen,
					input: node.data.terminalInput,
					lines: node.data.terminalLines,
				},
			]),
		);
	}, [nodes]);

	useEffect(() => {
		setNodes((curr) =>
			curr.map((node) => ({
				...node,
				data: {
					...node.data,
					isBusy: busyNodeIds.has(node.id),
				},
			})),
		);
	}, [busyNodeIds]);

	useEffect(() => {
		connectionSourceNodeIdRef.current = connectionSourceNodeId;
		setNodes((curr) =>
			curr.map((node) => ({
				...node,
				data: {
					...node.data,
					connectionSourceNodeId,
				},
			})),
		);
	}, [connectionSourceNodeId]);

	useEffect(() => {
		fullscreenTerminalNodeIdRef.current = fullscreenTerminalNodeId;
		setNodes((curr) =>
			curr.map((node) => ({
				...node,
				data: {
					...node.data,
					isTerminalFullscreen: fullscreenTerminalNodeId === node.id,
				},
			})),
		);
	}, [fullscreenTerminalNodeId]);

	useEffect(() => {
		setEdges((curr) => applySelectedEdge(curr, selectedLinkId));
	}, [selectedLinkId]);

	useEffect(() => {
		toggleNodeRunRef.current = toggleNodeRun;
	}, [toggleNodeRun]);

	const toggleTerminal = useCallback((nodeID: string) => {
		setNodes((curr) =>
			curr.map((node) =>
				node.id === nodeID
					? {
						...node,
						data: {
							...node.data,
							isTerminalOpen: node.data.status === "running" ? !node.data.isTerminalOpen : false,
						},
					}
					: node,
			),
		);
		setFullscreenTerminalNodeId((current) => (current === nodeID ? "" : current));
	}, []);

	const toggleTerminalFullscreen = useCallback((nodeID: string) => {
		setFullscreenTerminalNodeId((current) => (current === nodeID ? "" : nodeID));
	}, []);

	const updateTerminalInput = useCallback((nodeID: string, value: string) => {
		setNodes((curr) =>
			curr.map((node) =>
				node.id === nodeID
					? {
						...node,
						data: {
							...node.data,
							terminalInput: value,
						},
					}
					: node,
			),
		);
	}, []);

	const submitTerminalInput = useCallback(
		async (nodeID: string) => {
			const currentNode = nodesRef.current.find((node) => node.id === nodeID);
			if (!currentNode) {
				setStatus("Node not found in canvas");
				return;
			}

			const command = currentNode.data.terminalInput.trim();
			if (command === "") {
				return;
			}

			if (command === "clear") {
				setNodes((curr) =>
					curr.map((node) =>
						node.id === nodeID
							? {
								...node,
								data: {
									...node.data,
									terminalInput: "",
									terminalLines: [],
								},
							}
							: node,
					),
				);
				return;
			}

			setNodes((curr) =>
				curr.map((node) =>
					node.id === nodeID
						? {
							...node,
							data: {
								...node.data,
								terminalInput: "",
							},
						}
						: node,
				),
			);

			setBusy(true);
			try {
				const result: ApiCommandResponse = await runNodeCommand(baseUrl, nodeID, command);
				const outputLines = [
					...result.stdout
						.split("\n")
						.map((line) => line.trimEnd())
						.filter((line) => line !== ""),
					...result.stderr
						.split("\n")
						.map((line) => line.trimEnd())
						.filter((line) => line !== ""),
				];

				setNodes((curr) =>
					curr.map((node) =>
						node.id === nodeID
							? {
								...node,
								data: {
									...node.data,
									terminalLines: [...node.data.terminalLines, `$ ${command}`, ...outputLines],
								},
							}
							: node,
					),
				);
				setStatus(`Executed ${result.command} on ${nodeID}`);
			} catch (err: unknown) {
				const message = err instanceof Error ? err.message : String(err);
				setNodes((curr) =>
					curr.map((node) =>
						node.id === nodeID
							? {
								...node,
								data: {
									...node.data,
									terminalLines: [...node.data.terminalLines, `$ ${command}`, message],
								},
							}
							: node,
					),
				);
				setStatus(`Failed to execute command: ${message}`);
			} finally {
				setBusy(false);
			}
		},
		[baseUrl],
	);

	useEffect(() => {
		toggleTerminalRef.current = toggleTerminal;
	}, [toggleTerminal]);

	useEffect(() => {
		toggleTerminalFullscreenRef.current = toggleTerminalFullscreen;
	}, [toggleTerminalFullscreen]);

	useEffect(() => {
		updateTerminalInputRef.current = updateTerminalInput;
	}, [updateTerminalInput]);

	useEffect(() => {
		submitTerminalInputRef.current = submitTerminalInput;
	}, [submitTerminalInput]);

	useEffect(() => {
		selectedNodeIdRef.current = selectedNodeId;
	}, [selectedNodeId]);

	useEffect(() => {
		selectedLinkIdRef.current = selectedLinkId;
	}, [selectedLinkId]);

	useEffect(() => {
		pendingConnectionRef.current = pendingConnection;
	}, [pendingConnection]);

	useEffect(() => {
		if (fullscreenTerminalNodeId === "") {
			return;
		}
		const fullscreenNode = nodes.find((node) => node.id === fullscreenTerminalNodeId);
		if (!fullscreenNode || !fullscreenNode.data.isTerminalOpen) {
			setFullscreenTerminalNodeId("");
		}
	}, [fullscreenTerminalNodeId, nodes]);

	const onNodesChange: OnNodesChange = useCallback(
		(changes) => {
			setNodes((curr) =>
				applySelectedNode(applyNodeChanges(changes, curr), selectedNodeId),
			);
		},
		[selectedNodeId],
	);

	const onEdgesChange: OnEdgesChange = useCallback((changes) => {
		setEdges((curr) => applySelectedEdge(applyEdgeChanges(changes, curr), selectedLinkIdRef.current));
	}, []);

	const createNode = useCallback(async (nodeType: "host" | "switch" | "router") => {
		setBusy(true);
		setStatus(`Creating ${nodeType}...`);
		try {
			await createNodeRequest(baseUrl, nodeType, randomPos(nodesRef.current.length));
			setIsCreateNodeMenuOpen(false);
			await loadTopology();
		} catch (err: unknown) {
			const message = err instanceof Error ? err.message : String(err);
			setStatus(message || "Failed to create node");
		} finally {
			setBusy(false);
		}
	}, [baseUrl, loadTopology]);

	const deleteSelectedLink = useCallback(async () => {
		if (!selectedLinkId) {
			setStatus("Select a link first");
			return;
		}

		setBusy(true);
		setStatus(`Removing link ${selectedLinkId}...`);
		try {
			await deleteLink(baseUrl, selectedLinkId);
			setSelectedLinkId("");
			await loadTopology();
			setStatus("Link removed");
		} catch (err: unknown) {
			const message = err instanceof Error ? err.message : String(err);
			setStatus(message || "Failed to remove link");
		} finally {
			setBusy(false);
		}
	}, [baseUrl, loadTopology, selectedLinkId]);

	const saveTopologyToFile = useCallback(async () => {
		setBusy(true);
		setStatus("Saving topology...");
		try {
			const topology = await exportTopology(baseUrl);
			const blob = new Blob([JSON.stringify(topology, null, 2)], {
				type: "application/json",
			});
			const url = URL.createObjectURL(blob);
			const link = document.createElement("a");
			link.href = url;
			link.download = "topology.json";
			document.body.appendChild(link);
			link.click();
			document.body.removeChild(link);
			URL.revokeObjectURL(url);
			setStatus("Topology saved");
		} catch (err: unknown) {
			const message = err instanceof Error ? err.message : String(err);
			setStatus(message || "Failed to save topology");
		} finally {
			setBusy(false);
		}
	}, [baseUrl]);

	const openImportPicker = useCallback(() => {
		importInputRef.current?.click();
	}, []);

	const loadTopologyFromFile = useCallback(
		async (event: ChangeEvent<HTMLInputElement>) => {
			const file = event.target.files?.[0];
			event.target.value = "";
			if (!file) {
				return;
			}

			setBusy(true);
			setStatus(`Loading topology from ${file.name}...`);
			try {
				const raw = await file.text();
				const topology = JSON.parse(raw) as TopologyFile;
				await importTopology(baseUrl, topology);
				nodePositionsRef.current = new Map();
				await loadTopology();
				setSelectedNodeId("");
				setStatus("Topology loaded");
			} catch (err: unknown) {
				const message = err instanceof Error ? err.message : String(err);
				setStatus(message || "Failed to load topology");
			} finally {
				setBusy(false);
			}
		},
		[baseUrl, loadTopology],
	);

	const clearCurrentTopology = useCallback(async () => {
		setBusy(true);
		setStatus("Clearing topology...");
		try {
			await clearTopology(baseUrl);
			nodePositionsRef.current = new Map();
			terminalStateRef.current = new Map();
			nodesRef.current = [];
			selectedNodeIdRef.current = "";
			selectedLinkIdRef.current = "";
			connectionSourceNodeIdRef.current = "";
			fullscreenTerminalNodeIdRef.current = "";
			pendingConnectionRef.current = null;
			setNodes([]);
			setEdges([]);
			setSelectedNodeId("");
			setSelectedLinkId("");
			setConnectionSourceNodeId("");
			setFullscreenTerminalNodeId("");
			setPendingConnection(null);
			setBusyNodeIds(new Set());
			setIsCreateNodeMenuOpen(false);
			setStatus("Topology cleared");
		} catch (err: unknown) {
			const message = err instanceof Error ? err.message : String(err);
			setStatus(message || "Failed to clear topology");
		} finally {
			setBusy(false);
		}
	}, [baseUrl]);

	const deleteSelectedNode = useCallback(async () => {
		if (!selectedNodeId) {
			setStatus("Select a node first");
			return;
		}

		setBusy(true);
		setStatus(`Deleting node ${selectedNodeId}...`);
		try {
			await deleteNode(baseUrl, selectedNodeId);
			setSelectedNodeId("");
			setSelectedLinkId("");
			setNodes((curr) => applySelectedNode(curr, ""));
			await loadTopology();
		} catch (err: unknown) {
			const message = err instanceof Error ? err.message : String(err);
			setStatus(message || "Failed to delete node");
		} finally {
			setBusy(false);
		}
	}, [baseUrl, loadTopology, selectedNodeId]);

	useEffect(() => {
		function onKeyDown(event: KeyboardEvent) {
			if (event.key !== "Delete") {
				return;
			}

			const target = event.target;
			if (
				target instanceof HTMLElement &&
				(target.tagName === "INPUT" ||
					target.tagName === "TEXTAREA" ||
					target.isContentEditable)
			) {
				return;
			}

			event.preventDefault();
			void deleteSelectedNode();
		}

		window.addEventListener("keydown", onKeyDown);
		return () => {
			window.removeEventListener("keydown", onKeyDown);
		};
	}, [deleteSelectedNode]);

	const onConnect: OnConnect = useCallback(
		async (connection: Connection) => {
			const source = connection.source ?? "";
			const target = connection.target ?? "";
			if (!source || !target || source === target) {
				return;
			}

			const key =
				source < target ? `${source}|${target}` : `${target}|${source}`;
			const existing = edgeByPair.get(key);

			setBusy(true);
			try {
				if (existing) {
					setStatus("Interface is busy");
					return;
				}

				const sourceNode = nodesRef.current.find((node) => node.id === source);
				const targetNode = nodesRef.current.find((node) => node.id === target);
				if (!sourceNode || !targetNode) {
					setStatus("Node not found in canvas");
					return;
				}

				const sourceInterfaces = getAvailableInterfaces(sourceNode.data.interfaces);
				const targetInterfaces = getAvailableInterfaces(targetNode.data.interfaces);
				if (sourceInterfaces.length === 0 || targetInterfaces.length === 0) {
					setStatus("Interface is busy");
					return;
				}

				setPendingConnection({
					sourceNodeID: source,
					targetNodeID: target,
					sourceInterfaceID: sourceInterfaces[0].id,
					targetInterfaceID: targetInterfaces[0].id,
				});
				setStatus("Choose interfaces for the new link");
			} catch (err: unknown) {
				const message = err instanceof Error ? err.message : String(err);
				setStatus(message || "Failed to update link");
			} finally {
				setBusy(false);
			}
		},
		[baseUrl, edgeByPair],
	);

	const confirmPendingConnection = useCallback(async () => {
		if (!pendingConnection) {
			return;
		}

		setBusy(true);
		setStatus("Creating link...");
		try {
			await createLink(
				baseUrl,
				pendingConnection.sourceInterfaceID,
				pendingConnection.targetInterfaceID,
			);
			setPendingConnection(null);
			setConnectionSourceNodeId("");
			await loadTopology();
			setStatus("Link created");
		} catch (err: unknown) {
			const message = err instanceof Error ? err.message : String(err);
			setStatus(message || "Failed to create link");
		} finally {
			setBusy(false);
		}
	}, [baseUrl, loadTopology, pendingConnection]);

	const updatePendingConnection = useCallback(
		(key: "sourceInterfaceID" | "targetInterfaceID", value: string) => {
			setPendingConnection((current) =>
				current
					? {
						...current,
						[key]: value,
					}
					: current,
			);
		},
		[],
	);

	const persistNodePosition = useCallback(
		async (nodeID: string, position: { x: number; y: number }) => {
			try {
				await updateNodePosition(baseUrl, nodeID, position);
			} catch (err: unknown) {
				const message = err instanceof Error ? err.message : String(err);
				setStatus(`Failed to persist node position: ${message}`);
			}
		},
		[baseUrl],
	);

	const sourceInterfaceOptions = pendingConnection
		? getAvailableInterfaces(
			nodes.find((node) => node.id === pendingConnection.sourceNodeID)?.data.interfaces ?? [],
		)
		: [];
	const targetInterfaceOptions = pendingConnection
		? getAvailableInterfaces(
			nodes.find((node) => node.id === pendingConnection.targetNodeID)?.data.interfaces ?? [],
		)
		: [];

	return (
		<div className="relative h-screen w-screen bg-zinc-100 text-zinc-900">
			<header className="fixed left-0 top-0 z-[2000] flex w-full items-center gap-3 border-b border-zinc-300 bg-white px-4 py-3 relative">
				<input
					ref={importInputRef}
					type="file"
					accept="application/json"
					onChange={(event) => {
						void loadTopologyFromFile(event);
					}}
					className="hidden"
				/>
				<div className="relative">
					<button
						type="button"
						onClick={() => {
							setIsCreateNodeMenuOpen((current) => !current);
						}}
						disabled={busy}
						className="rounded border border-zinc-700 px-3 py-1 text-sm hover:bg-zinc-100 disabled:opacity-60"
					>
						Create node
					</button>
					{isCreateNodeMenuOpen ? (
						<div className="absolute left-0 top-full z-30 mt-2 flex min-w-[140px] flex-col rounded border border-zinc-300 bg-white p-1 shadow-lg">
							<button
								type="button"
								onClick={() => void createNode("host")}
								className="rounded px-3 py-2 text-left text-sm hover:bg-zinc-100"
							>
								Host
							</button>
							<button
								type="button"
								onClick={() => void createNode("switch")}
								className="rounded px-3 py-2 text-left text-sm hover:bg-zinc-100"
							>
								Switch
							</button>
							<button
								type="button"
								onClick={() => void createNode("router")}
								className="rounded px-3 py-2 text-left text-sm hover:bg-zinc-100"
							>
								Router
							</button>
						</div>
					) : null}
				</div>
				<button
					type="button"
					onClick={() => void deleteSelectedNode()}
					disabled={busy || !selectedNodeId}
					className="rounded border border-zinc-700 px-3 py-1 text-sm hover:bg-zinc-100 disabled:opacity-60"
				>
					Delete node
				</button>
				<button
					type="button"
					onClick={() => void deleteSelectedLink()}
					disabled={busy || !selectedLinkId}
					className="rounded border border-zinc-700 px-3 py-1 text-sm hover:bg-zinc-100 disabled:opacity-60"
				>
					Unlink
				</button>
				<div className="pointer-events-none absolute left-1/2 top-1/2 -translate-x-1/2 -translate-y-1/2 text-sm text-zinc-600">
					{status}
				</div>
				<div className="ml-auto flex items-center gap-3">
					<button
						type="button"
						onClick={() => void saveTopologyToFile()}
						disabled={busy || nodes.length === 0}
						className="rounded border border-zinc-700 px-3 py-1 text-sm hover:bg-zinc-100 disabled:opacity-60"
					>
						Save
					</button>
					<button
						type="button"
						onClick={openImportPicker}
						disabled={busy}
						className="rounded border border-zinc-700 px-3 py-1 text-sm hover:bg-zinc-100 disabled:opacity-60"
					>
						Load
					</button>
					<button
						type="button"
						onClick={() => void clearCurrentTopology()}
						disabled={busy}
						className="rounded border border-zinc-700 px-3 py-1 text-sm hover:bg-zinc-100 disabled:opacity-60"
					>
						Clear
					</button>
				</div>
			</header>

			<main className="absolute inset-x-0 bottom-0 top-14">
				{pendingConnection ? (
					<div className="fixed left-1/2 top-1/2 z-30 w-[360px] -translate-x-1/2 -translate-y-1/2 rounded border border-zinc-300 bg-white p-4 shadow-lg">
						<div className="mb-3 text-sm font-semibold text-zinc-900">Choose interfaces</div>
						<div className="mb-3 flex flex-col gap-1">
							<label className="text-xs text-zinc-600" htmlFor="source-interface">
								Source interface
							</label>
							<select
								id="source-interface"
								value={pendingConnection.sourceInterfaceID}
								onChange={(event) => updatePendingConnection("sourceInterfaceID", event.target.value)}
								className="rounded border border-zinc-300 px-2 py-1 text-sm"
							>
								{sourceInterfaceOptions.map((iface) => (
									<option key={iface.id} value={iface.id}>
										{iface.name}
									</option>
								))}
							</select>
						</div>
						<div className="mb-4 flex flex-col gap-1">
							<label className="text-xs text-zinc-600" htmlFor="target-interface">
								Target interface
							</label>
							<select
								id="target-interface"
								value={pendingConnection.targetInterfaceID}
								onChange={(event) => updatePendingConnection("targetInterfaceID", event.target.value)}
								className="rounded border border-zinc-300 px-2 py-1 text-sm"
							>
								{targetInterfaceOptions.map((iface) => (
									<option key={iface.id} value={iface.id}>
										{iface.name}
									</option>
								))}
							</select>
						</div>
						<div className="flex justify-end gap-2">
							<button
								type="button"
								onClick={() => {
									setPendingConnection(null);
									setConnectionSourceNodeId("");
								}}
								className="rounded border border-zinc-300 px-3 py-1 text-sm hover:bg-zinc-100"
							>
								Cancel
							</button>
							<button
								type="button"
								onClick={() => void confirmPendingConnection()}
								disabled={busy}
								className="rounded border border-zinc-700 px-3 py-1 text-sm hover:bg-zinc-100 disabled:opacity-60"
							>
								Create link
							</button>
						</div>
					</div>
				) : null}
				<ReactFlow
					nodes={nodes}
					edges={edges}
					nodeTypes={nodeTypes}
					edgeTypes={edgeTypes}
					onNodesChange={onNodesChange}
					onEdgesChange={onEdgesChange}
					onConnect={(connection) => {
						void onConnect(connection);
					}}
					onConnectStart={(_, params) => {
						setSelectedLinkId("");
						setConnectionSourceNodeId(params.nodeId ?? "");
					}}
					onConnectEnd={() => {
						if (!pendingConnectionRef.current) {
							setConnectionSourceNodeId("");
						}
					}}
					onNodeClick={(_, node) => {
						setSelectedNodeId(node.id);
						setSelectedLinkId("");
						setNodes((curr) => applySelectedNode(curr, node.id));
					}}
					onEdgeClick={((_, edge) => {
						setSelectedNodeId("");
						setSelectedLinkId(edge.id);
						setNodes((curr) => applySelectedNode(curr, ""));
					}) as EdgeMouseHandler}
					onNodeDragStop={(_, node) => {
						void persistNodePosition(node.id, {
							x: node.position.x,
							y: node.position.y,
						});
					}}
					onPaneClick={() => {
						setSelectedNodeId("");
						setSelectedLinkId("");
						setNodes((curr) => applySelectedNode(curr, ""));
					}}
					zoomOnScroll
					connectionLineType={ConnectionLineType.Straight}
					defaultEdgeOptions={{ type: "interfaceLabel", style: EDGE_STYLE }}
					nodesConnectable
					fitView
					defaultViewport={{ x: 0, y: 0, zoom: 1 }}
				>
					<MiniMap zoomable pannable nodeStrokeWidth={3} nodeColor="#e5e7eb" />
					<Controls />
					<Background gap={18} size={1} />
				</ReactFlow>
			</main>
		</div>
	);
}
