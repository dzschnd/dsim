import { useCallback, useEffect, useMemo, useRef, useState, type ChangeEvent, type MouseEvent as ReactMouseEvent } from "react";
import { CircleHelp, FileUp, Monitor, Network, RotateCcw, Router, Save } from "lucide-react";
import ReactFlow, {
	Background,
	type Connection,
	ConnectionLineType,
	Controls,
	type Edge,
	type EdgeMouseHandler,
	MiniMap,
	type Node,
	type OnConnect,
	type OnEdgesChange,
	type OnNodesChange,
	applyEdgeChanges,
	applyNodeChanges,
} from "reactflow";
import { InterfaceLabelEdge, type InterfaceLabelEdgeData } from "./InterfaceLabelEdge";
import { Sidebar, type SidebarLastCommand } from "./Sidebar";
import { SquareNode, type SquareNodeData } from "./SquareNode";
import {
	TerminalPanel,
	getTerminalPanelHeight,
	type TerminalPanelState,
	type TerminalStatus,
	type TerminalTab,
} from "./TerminalPanel";
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
	fetchNode,
	fetchTopology,
	importTopology,
	runNodeCommand,
	startNode,
	stopNode,
	updateNodeName,
	updateNodePosition,
} from "../services/topology";

type PendingConnection = {
	sourceNodeID: string;
	targetNodeID: string;
	sourceInterfaceID: string;
	targetInterfaceID: string;
};

type ConfirmAction = "delete-node" | "clear-topology";

const EDGE_STYLE = {
	stroke: "#334155",
	strokeWidth: 3,
};

const SELECTED_EDGE_STYLE = {
	stroke: "#6b8fd6",
	strokeWidth: 4,
};
const HEADER_HEIGHT = 56;
const TERMINAL_HEADER_HEIGHT = 44;
const TERMINAL_RESIZE_HANDLE_HEIGHT = 1;
const DEFAULT_TERMINAL_BODY_HEIGHT = 224;
function randomPos(index: number) {
	const row = Math.floor(index / 5);
	const col = index % 5;
	return { x: 80 + col * 220, y: 100 + row * 220 };
}

function applySelectedNode(
	nodes: Node<SquareNodeData>[],
	selectedNodeId: string,
): Node<SquareNodeData>[] {
	return nodes.map((node) => ({
		...node,
		selected: node.id === selectedNodeId,
		data: { ...node.data, isSelected: node.id === selectedNodeId },
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
	return nodes.find((n) => n.data.interfaces.some((i) => i.id === interfaceID))?.id ?? "";
}

function findInterfaceNameByID(nodes: Node<SquareNodeData>[], interfaceID: string): string {
	for (const node of nodes) {
		const iface = node.data.interfaces.find((i) => i.id === interfaceID);
		if (iface) return iface.name;
	}
	return "";
}

function findInterfaceCIDRByID(nodes: Node<SquareNodeData>[], interfaceID: string): string {
	for (const node of nodes) {
		const iface = node.data.interfaces.find((i) => i.id === interfaceID);
		if (iface && iface.ipAddress !== "" && iface.prefixLength > 0) {
			return `${iface.ipAddress}/${iface.prefixLength}`;
		}
	}
	return "";
}

function isInterfaceAddressCommand(command: string): boolean {
	const f = command.trim().split(/\s+/);
	return (f.length === 4 && f[0] === "ip" && f[1] === "set")
		|| (f.length === 3 && f[0] === "ip" && f[1] === "unset");
}

function isNodeStateCommand(command: string): boolean {
	return command.trim() === "freeze" || command.trim() === "unfreeze";
}

function appendTerminalHistory(history: string[], command: string): string[] {
	const normalized = command.trim();
	if (normalized === "") return history;
	if (history.at(-1)?.trim() === normalized) return history;
	return [...history, normalized];
}

const nodeTypes = { square: SquareNode };
const edgeTypes = { interfaceLabel: InterfaceLabelEdge };

export function TopologyCanvas() {
	const baseUrl = import.meta.env.VITE_API_BASE_URL ?? "";

	const [nodes, setNodes] = useState<Node<SquareNodeData>[]>([]);
	const [edges, setEdges] = useState<Edge[]>([]);
	const [selectedNodeId, setSelectedNodeId] = useState<string>("");
	const [selectedLinkId, setSelectedLinkId] = useState<string>("");
	const [pendingConnection, setPendingConnection] = useState<PendingConnection | null>(null);
	const [confirmAction, setConfirmAction] = useState<ConfirmAction | null>(null);
	const [connectionSourceNodeId, setConnectionSourceNodeId] = useState<string>("");
	const [isCreateNodeMenuOpen, setIsCreateNodeMenuOpen] = useState<boolean>(false);
	const [busyNodeIds, setBusyNodeIds] = useState<Set<string>>(new Set());
	const [busy, setBusy] = useState<boolean>(false);
	const [status, setStatus] = useState<string>("");
	const [lastCommand, setLastCommand] = useState<SidebarLastCommand | null>(null);
	const [nodeNames, setNodeNames] = useState<Record<string, string>>({});
	const [sidebarCollapsed, setSidebarCollapsed] = useState<boolean>(true);
	const [isTerminalResizing, setIsTerminalResizing] = useState<boolean>(false);

	// Terminal panel state
	const [terminalTabs, setTerminalTabs] = useState<TerminalTab[]>([]);
	const [activeTabId, setActiveTabId] = useState<number | null>(null);
	const [terminalPanelState, setTerminalPanelState] = useState<TerminalPanelState>("hidden");
	const [terminalBodyHeight, setTerminalBodyHeight] = useState<number>(DEFAULT_TERMINAL_BODY_HEIGHT);
	const nextTabIdRef = useRef<number>(1);
	const createNodeMenuRef = useRef<HTMLDivElement | null>(null);

	const selectedNodeIdRef = useRef<string>("");
	const selectedLinkIdRef = useRef<string>("");
	const importInputRef = useRef<HTMLInputElement | null>(null);
	const connectionSourceNodeIdRef = useRef<string>("");
	const pendingConnectionRef = useRef<PendingConnection | null>(null);
	const nodePositionsRef = useRef<Map<string, { x: number; y: number }>>(new Map());
	const nodesRef = useRef<Node<SquareNodeData>[]>([]);
	const terminalTabsRef = useRef<TerminalTab[]>([]);
	const activeTabIdRef = useRef<number | null>(null);

	// Refs for node button callbacks
	const toggleNodeRunRef = useRef<(nodeId: string) => void>(() => { });
	const openTerminalForNodeRef = useRef<(nodeId: string) => void>(() => { });

	const buildFlowNode = useCallback(
		(
			node: { id: string; name: string; position: { x: number; y: number }; status: string; type: string; interfaces: ApiInterface[] },
			position: { x: number; y: number },
			selectedNodeId: string,
		): Node<SquareNodeData> => ({
			id: node.id,
			type: "square",
			position,
			zIndex: 10,
			selected: node.id === selectedNodeId,
			data: {
				nodeId: node.id,
				displayName: nodeNames[node.id] ?? (node.name || node.id),
				status: node.status,
				type: node.type,
				interfaces: node.interfaces,
				isSelected: node.id === selectedNodeId,
				isBusy: busyNodeIds.has(node.id),
				connectionSourceNodeId: connectionSourceNodeIdRef.current,
				onToggleRun: () => toggleNodeRunRef.current(node.id),
				onOpenTerminal: () => openTerminalForNodeRef.current(node.id),
			},
		}),
		[busyNodeIds, nodeNames],
	);

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

	const setRequestStatus = useCallback((message = "Loading...") => setStatus(message), []);

	const loadTopology = useCallback(async () => {
		setBusy(true);
		setRequestStatus("Loading topology...");
		try {
			const { nodes: apiNodes, links: apiLinks } = await fetchTopology(baseUrl);
			const existingPositions = nodePositionsRef.current;
			const currentSelectedNodeId = selectedNodeIdRef.current;

			const flowNodes: Node<SquareNodeData>[] = apiNodes.map((node, index) =>
				buildFlowNode(
					node,
					existingPositions.get(node.id) ?? node.position ?? randomPos(index),
					currentSelectedNodeId,
				),
			);

			const flowEdges: Edge<InterfaceLabelEdgeData>[] = apiLinks.map((link) => ({
				id: link.id,
				type: "interfaceLabel",
				source: findNodeIDByInterfaceID(flowNodes, link.interfaceAId),
				target: findNodeIDByInterfaceID(flowNodes, link.interfaceBId),
				style: link.id === selectedLinkIdRef.current ? SELECTED_EDGE_STYLE : EDGE_STYLE,
				data: {
					interfaceAId: link.interfaceAId,
					interfaceAName: findInterfaceNameByID(flowNodes, link.interfaceAId),
					interfaceAIP: findInterfaceCIDRByID(flowNodes, link.interfaceAId),
					interfaceBId: link.interfaceBId,
					interfaceBName: findInterfaceNameByID(flowNodes, link.interfaceBId),
					interfaceBIP: findInterfaceCIDRByID(flowNodes, link.interfaceBId),
				},
			}));

			setNodes(applySelectedNode(flowNodes, currentSelectedNodeId));
			setEdges(applySelectedEdge(flowEdges, selectedLinkIdRef.current));
			setStatus(`Loaded ${flowNodes.length} nodes, ${flowEdges.length} links`);
		} catch (err: unknown) {
			setStatus((err instanceof Error ? err.message : String(err)) || "Failed to load topology");
		} finally {
			setBusy(false);
		}
	}, [baseUrl, buildFlowNode, setRequestStatus]);

	const refreshNode = useCallback(
		async (nodeID: string) => {
			setRequestStatus(`Refreshing node ${nodeID}...`);
			const apiNode = await fetchNode(baseUrl, nodeID);
			const currentSelectedNodeId = selectedNodeIdRef.current;
			const currentNode = nodesRef.current.find((n) => n.id === nodeID);
			const position = currentNode?.position ?? nodePositionsRef.current.get(nodeID) ?? apiNode.position;

			let nextNodes: Node<SquareNodeData>[] = [];
			setNodes((curr) => {
				nextNodes = curr.map((node) =>
					node.id === nodeID ? buildFlowNode(apiNode, position, currentSelectedNodeId) : node,
				);
				return applySelectedNode(nextNodes, currentSelectedNodeId);
			});
			if (nextNodes.length === 0) return;
			setEdges((curr) =>
				applySelectedEdge(
					curr.map((edge) => {
						const d = edge.data as InterfaceLabelEdgeData | undefined;
						if (!d?.interfaceAId || !d.interfaceBId) return edge;
						return {
							...edge,
							data: {
								...d,
								interfaceAName: findInterfaceNameByID(nextNodes, d.interfaceAId),
								interfaceAIP: findInterfaceCIDRByID(nextNodes, d.interfaceAId),
								interfaceBName: findInterfaceNameByID(nextNodes, d.interfaceBId),
								interfaceBIP: findInterfaceCIDRByID(nextNodes, d.interfaceBId),
							},
						};
					}),
					selectedLinkIdRef.current,
				),
			);
		},
		[baseUrl, buildFlowNode, setRequestStatus],
	);

	const setNodeBusy = useCallback((nodeID: string, nextBusy: boolean) => {
		setBusyNodeIds((curr) => {
			const updated = new Set(curr);
			if (nextBusy) updated.add(nodeID);
			else updated.delete(nodeID);
			return updated;
		});
	}, []);

	const toggleNodeRun = useCallback(
		async (nodeID: string) => {
			const currentNode = nodesRef.current.find((n) => n.id === nodeID);
			if (!currentNode) return;
			const { status: nodeStatus } = currentNode.data;
			const action = nodeStatus === "running" || nodeStatus === "frozen" ? "stop" : "start";
			setNodeBusy(nodeID, true);
			setRequestStatus(`${action === "start" ? "Starting" : "Stopping"} node ${nodeID}...`);
			try {
				if (action === "start") await startNode(baseUrl, nodeID);
				else await stopNode(baseUrl, nodeID);
				await loadTopology();
				setStatus(`Node ${nodeID} ${action === "start" ? "started" : "stopped"}`);
			} catch (err: unknown) {
				setStatus(`Failed to ${action} node: ${err instanceof Error ? err.message : String(err)}`);
			} finally {
				setNodeBusy(nodeID, false);
			}
		},
		[baseUrl, loadTopology, setNodeBusy, setRequestStatus],
	);

	const toggleFreezeNode = useCallback(
		async (nodeID: string) => {
			const currentNode = nodesRef.current.find((n) => n.id === nodeID);
			if (!currentNode) return;
			const { status: nodeStatus } = currentNode.data;
			if (nodeStatus === "stopped") return;
			const command = nodeStatus === "frozen" ? "unfreeze" : "freeze";
			setNodeBusy(nodeID, true);
			setRequestStatus(`${command === "freeze" ? "Freezing" : "Unfreezing"} node ${nodeID}...`);
			try {
				await runNodeCommand(baseUrl, nodeID, command);
				await refreshNode(nodeID);
				setStatus(`Node ${nodeID} ${command === "freeze" ? "frozen" : "unfrozen"}`);
			} catch (err: unknown) {
				setStatus(`Failed to ${command} node: ${err instanceof Error ? err.message : String(err)}`);
			} finally {
				setNodeBusy(nodeID, false);
			}
		},
		[baseUrl, refreshNode, setNodeBusy, setRequestStatus],
	);

	// Terminal tab management
	const openTerminalForNode = useCallback((nodeId: string) => {
		const existing = terminalTabsRef.current.find((t) => t.nodeId === nodeId);
		if (existing) {
			setActiveTabId(existing.tabId);
			setTerminalPanelState((s) => s === "hidden" ? "normal" : s);
			return;
		}
		const tabId = nextTabIdRef.current++;
		setTerminalTabs((curr) => [...curr, { tabId, nodeId, lines: [], input: "", history: [], historyIndex: null, historyDraft: null }]);
		setActiveTabId(tabId);
		setTerminalPanelState("normal");
	}, []);

	const closeTerminalTab = useCallback((tabId: number) => {
		setTerminalTabs((curr) => curr.filter((t) => t.tabId !== tabId));
		setActiveTabId((current) => {
			if (current !== tabId) return current;
			const remaining = terminalTabsRef.current.filter((t) => t.tabId !== tabId);
			return remaining.at(-1)?.tabId ?? null;
		});
	}, []);

	const handleTabInputChange = useCallback((tabId: number, value: string) => {
		setTerminalTabs((curr) => curr.map((t) => t.tabId === tabId ? { ...t, input: value } : t));
	}, []);

	const handleTabHistoryNavigate = useCallback((tabId: number, direction: "up" | "down") => {
		setTerminalTabs((curr) =>
			curr.map((t) => {
				if (t.tabId !== tabId) return t;
				const { history, historyIndex, historyDraft } = t;
				if (direction === "up") {
					if (history.length === 0) return t;
					if (historyIndex === null) {
						return { ...t, input: history[history.length - 1], historyIndex: history.length - 1, historyDraft: t.input };
					}
					if (historyIndex === 0) return t;
					const prev = historyIndex - 1;
					return { ...t, input: history[prev], historyIndex: prev };
				}
				if (historyIndex === null) return t;
				const next = historyIndex + 1;
				if (next < history.length) return { ...t, input: history[next], historyIndex: next };
				return { ...t, input: historyDraft ?? "", historyIndex: null, historyDraft: null };
			}),
		);
	}, []);

	const submitTerminalTab = useCallback(
		async (tabId: number) => {
			const tab = terminalTabsRef.current.find((t) => t.tabId === tabId);
			if (!tab) return;
			const { nodeId } = tab;
			const command = tab.input.trim();
			if (!command) return;

			if (command === "clear") {
				setTerminalTabs((curr) => curr.map((t) =>
					t.tabId === tabId
						? { ...t, input: "", lines: [], history: appendTerminalHistory(t.history, command), historyIndex: null, historyDraft: null }
						: t,
				));
				return;
			}

			if (command === "history") {
				setTerminalTabs((curr) => curr.map((t) =>
					t.tabId === tabId
						? { ...t, input: "", history: appendTerminalHistory(t.history, command), historyIndex: null, historyDraft: null, lines: [...t.lines, "$ history", ...t.history] }
						: t,
				));
				return;
			}

			setTerminalTabs((curr) => curr.map((t) =>
				t.tabId === tabId ? { ...t, input: "", historyIndex: null, historyDraft: null } : t,
			));

			setLastCommand({ command, status: "executing" });
			setBusy(true);
			setRequestStatus(`Running command on ${nodeId}...`);
			try {
				const result: ApiCommandResponse = await runNodeCommand(baseUrl, nodeId, command);
				const outputLines = [
					...result.stdout.split("\n").map((l) => l.trimEnd()).filter((l) => l !== ""),
					...result.stderr.split("\n").map((l) => l.trimEnd()).filter((l) => l !== ""),
				];
				setTerminalTabs((curr) => curr.map((t) =>
					t.tabId === tabId ? { ...t, lines: [...t.lines, `$ ${command}`, ...outputLines] } : t,
				));
				if (isInterfaceAddressCommand(command) || isNodeStateCommand(command)) {
					await refreshNode(nodeId);
				}
				setStatus(`Executed ${result.command} on ${nodeId}`);
				setLastCommand({ command, status: "success" });
			} catch (err: unknown) {
				const message = err instanceof Error ? err.message : String(err);
				setTerminalTabs((curr) => curr.map((t) =>
					t.tabId === tabId ? { ...t, lines: [...t.lines, `$ ${command}`, message] } : t,
				));
				setStatus(`Failed to execute command: ${message}`);
				setLastCommand({ command, status: "failed" });
			} finally {
				setTerminalTabs((curr) => curr.map((t) =>
					t.tabId === tabId ? { ...t, history: appendTerminalHistory(t.history, command) } : t,
				));
				setBusy(false);
			}
		},
		[baseUrl, refreshNode, setRequestStatus],
	);

	// Sync refs
	useEffect(() => { selectedNodeIdRef.current = selectedNodeId; }, [selectedNodeId]);
	useEffect(() => { selectedLinkIdRef.current = selectedLinkId; }, [selectedLinkId]);
	useEffect(() => { pendingConnectionRef.current = pendingConnection; }, [pendingConnection]);
	useEffect(() => { terminalTabsRef.current = terminalTabs; }, [terminalTabs]);
	useEffect(() => { activeTabIdRef.current = activeTabId; }, [activeTabId]);
	useEffect(() => { toggleNodeRunRef.current = (nodeId) => void toggleNodeRun(nodeId); }, [toggleNodeRun]);
	useEffect(() => { openTerminalForNodeRef.current = openTerminalForNode; }, [openTerminalForNode]);

	useEffect(() => { void loadTopology(); }, [loadTopology]);

	useEffect(() => {
		nodePositionsRef.current = new Map(nodes.map((n) => [n.id, { x: n.position.x, y: n.position.y }]));
		nodesRef.current = nodes;
	}, [nodes]);

	useEffect(() => {
		setNodes((curr) => curr.map((node) => ({
			...node,
			data: { ...node.data, isBusy: busyNodeIds.has(node.id) },
		})));
	}, [busyNodeIds]);

	useEffect(() => {
		connectionSourceNodeIdRef.current = connectionSourceNodeId;
		setNodes((curr) => curr.map((node) => ({
			...node,
			data: { ...node.data, connectionSourceNodeId },
		})));
	}, [connectionSourceNodeId]);

	useEffect(() => {
		setEdges((curr) => applySelectedEdge(curr, selectedLinkId));
	}, [selectedLinkId]);

	const onNodesChange: OnNodesChange = useCallback(
		(changes) => setNodes((curr) => applySelectedNode(applyNodeChanges(changes, curr), selectedNodeId)),
		[selectedNodeId],
	);

	const onEdgesChange: OnEdgesChange = useCallback(
		(changes) => setEdges((curr) => applySelectedEdge(applyEdgeChanges(changes, curr), selectedLinkIdRef.current)),
		[],
	);

	const createNode = useCallback(async (nodeType: "host" | "switch" | "router") => {
		setBusy(true);
		setRequestStatus(`Creating ${nodeType}...`);
		try {
			await createNodeRequest(baseUrl, nodeType, randomPos(nodesRef.current.length));
			setIsCreateNodeMenuOpen(false);
			await loadTopology();
		} catch (err: unknown) {
			setStatus((err instanceof Error ? err.message : String(err)) || "Failed to create node");
		} finally {
			setBusy(false);
		}
	}, [baseUrl, loadTopology, setRequestStatus]);

	const deleteSelectedLink = useCallback(async () => {
		if (!selectedLinkId) return;
		setBusy(true);
		setRequestStatus(`Removing link ${selectedLinkId}...`);
		try {
			await deleteLink(baseUrl, selectedLinkId);
			setSelectedLinkId("");
			setSidebarCollapsed(true);
			await loadTopology();
			setStatus("Link removed");
		} catch (err: unknown) {
			setStatus((err instanceof Error ? err.message : String(err)) || "Failed to remove link");
		} finally {
			setBusy(false);
		}
	}, [baseUrl, loadTopology, selectedLinkId, setRequestStatus]);

	const saveTopologyToFile = useCallback(async () => {
		setBusy(true);
		setRequestStatus("Saving topology...");
		try {
			const topology = await exportTopology(baseUrl);
			const blob = new Blob([JSON.stringify(topology, null, 2)], { type: "application/json" });
			const url = URL.createObjectURL(blob);
			const a = document.createElement("a");
			a.href = url;
			a.download = "topology.json";
			document.body.appendChild(a);
			a.click();
			document.body.removeChild(a);
			URL.revokeObjectURL(url);
			setStatus("Topology saved");
		} catch (err: unknown) {
			setStatus((err instanceof Error ? err.message : String(err)) || "Failed to save topology");
		} finally {
			setBusy(false);
		}
	}, [baseUrl, setRequestStatus]);

	const openImportPicker = useCallback(() => { importInputRef.current?.click(); }, []);

	const loadTopologyFromFile = useCallback(
		async (event: ChangeEvent<HTMLInputElement>) => {
			const file = event.target.files?.[0];
			event.target.value = "";
			if (!file) return;
			setBusy(true);
			setRequestStatus(`Loading topology from ${file.name}...`);
			try {
				const topology = JSON.parse(await file.text()) as TopologyFile;
				await importTopology(baseUrl, topology);
				nodePositionsRef.current = new Map();
				await loadTopology();
				setSelectedNodeId("");
				setStatus("Topology loaded");
			} catch (err: unknown) {
				setStatus((err instanceof Error ? err.message : String(err)) || "Failed to load topology");
			} finally {
				setBusy(false);
			}
		},
		[baseUrl, loadTopology, setRequestStatus],
	);

	const requestClearTopology = useCallback(() => { if (!busy) setConfirmAction("clear-topology"); }, [busy]);

	const clearCurrentTopology = useCallback(async () => {
		if (busy) return;
		setBusy(true);
		setConfirmAction(null);
		setRequestStatus("Clearing topology...");
		try {
			await clearTopology(baseUrl);
			nodePositionsRef.current = new Map();
			nodesRef.current = [];
			selectedNodeIdRef.current = "";
			selectedLinkIdRef.current = "";
			connectionSourceNodeIdRef.current = "";
			pendingConnectionRef.current = null;
			setNodes([]);
			setEdges([]);
			setSelectedNodeId("");
			setSelectedLinkId("");
			setConnectionSourceNodeId("");
			setPendingConnection(null);
			setBusyNodeIds(new Set());
			setIsCreateNodeMenuOpen(false);
			setTerminalTabs([]);
			setActiveTabId(null);
			setStatus("Topology cleared");
			setSidebarCollapsed(true);
		} catch (err: unknown) {
			setStatus((err instanceof Error ? err.message : String(err)) || "Failed to clear topology");
		} finally {
			setBusy(false);
		}
	}, [baseUrl, busy, setRequestStatus]);

	const requestDeleteSelectedNode = useCallback(() => {
		if (!selectedNodeId) { setStatus("Select a node first"); return; }
		if (!busy) setConfirmAction("delete-node");
	}, [busy, selectedNodeId]);

	const deleteSelectedNode = useCallback(async () => {
		if (busy || !selectedNodeId) {
			setConfirmAction(null);
			return;
		}
		setBusy(true);
		setConfirmAction(null);
		setRequestStatus(`Deleting node ${selectedNodeId}...`);
		try {
			const nodeIdToDelete = selectedNodeId;
			await deleteNode(baseUrl, nodeIdToDelete);
			setTerminalTabs((curr) => curr.filter((t) => t.nodeId !== nodeIdToDelete));
			setActiveTabId((current) => {
				const remaining = terminalTabsRef.current.filter((t) => t.nodeId !== nodeIdToDelete);
				if (remaining.some((t) => t.tabId === current)) return current;
				return remaining.at(-1)?.tabId ?? null;
			});
			setSelectedNodeId("");
			setSelectedLinkId("");
			setSidebarCollapsed(true);
			setNodes((curr) => applySelectedNode(curr, ""));
			await loadTopology();
		} catch (err: unknown) {
			setStatus((err instanceof Error ? err.message : String(err)) || "Failed to delete node");
		} finally {
			setBusy(false);
		}
	}, [baseUrl, busy, loadTopology, selectedNodeId, setRequestStatus]);

	useEffect(() => {
		function onKeyDown(event: KeyboardEvent) {
			if (event.key !== "Delete") return;
			const t = event.target;
			if (t instanceof HTMLElement && (t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.isContentEditable)) return;
			event.preventDefault();
			if (selectedLinkIdRef.current) { void deleteSelectedLink(); return; }
			requestDeleteSelectedNode();
		}
		window.addEventListener("keydown", onKeyDown);
		return () => window.removeEventListener("keydown", onKeyDown);
	}, [deleteSelectedLink, requestDeleteSelectedNode]);

	const onConnect: OnConnect = useCallback(
		async (connection: Connection) => {
			const source = connection.source ?? "";
			const target = connection.target ?? "";
			if (!source || !target || source === target) return;
			const key = source < target ? `${source}|${target}` : `${target}|${source}`;
			const existing = edgeByPair.get(key);
			setBusy(true);
			setRequestStatus("Loading...");
			try {
				if (existing) { setStatus("Interface is busy"); return; }
				const sourceNode = nodesRef.current.find((n) => n.id === source);
				const targetNode = nodesRef.current.find((n) => n.id === target);
				if (!sourceNode || !targetNode) { setStatus("Node not found in canvas"); return; }
				const si = getAvailableInterfaces(sourceNode.data.interfaces);
				const ti = getAvailableInterfaces(targetNode.data.interfaces);
				if (si.length === 0 || ti.length === 0) { setStatus("Interface is busy"); return; }
				setPendingConnection({ sourceNodeID: source, targetNodeID: target, sourceInterfaceID: si[0].id, targetInterfaceID: ti[0].id });
				setStatus("Choose interfaces for the new link");
			} catch (err: unknown) {
				setStatus((err instanceof Error ? err.message : String(err)) || "Failed to update link");
			} finally {
				setBusy(false);
			}
		},
		[edgeByPair, setRequestStatus],
	);

	const confirmPendingConnection = useCallback(async () => {
		if (!pendingConnection) return;
		setBusy(true);
		setRequestStatus("Creating link...");
		try {
			await createLink(baseUrl, pendingConnection.sourceInterfaceID, pendingConnection.targetInterfaceID);
			setPendingConnection(null);
			setConnectionSourceNodeId("");
			await loadTopology();
			setStatus("Link created");
		} catch (err: unknown) {
			setStatus((err instanceof Error ? err.message : String(err)) || "Failed to create link");
		} finally {
			setBusy(false);
		}
	}, [baseUrl, loadTopology, pendingConnection, setRequestStatus]);

	const updatePendingConnection = useCallback(
		(key: "sourceInterfaceID" | "targetInterfaceID", value: string) => {
			setPendingConnection((curr) => curr ? { ...curr, [key]: value } : curr);
		},
		[],
	);

	const persistNodePosition = useCallback(
		async (nodeID: string, position: { x: number; y: number }) => {
			try {
				await updateNodePosition(baseUrl, nodeID, position);
			} catch (err: unknown) {
				setStatus(`Failed to persist node position: ${err instanceof Error ? err.message : String(err)}`);
			}
		},
		[baseUrl],
	);

	const sourceInterfaceOptions = pendingConnection
		? getAvailableInterfaces(nodes.find((n) => n.id === pendingConnection.sourceNodeID)?.data.interfaces ?? [])
		: [];
	const targetInterfaceOptions = pendingConnection
		? getAvailableInterfaces(nodes.find((n) => n.id === pendingConnection.targetNodeID)?.data.interfaces ?? [])
		: [];

	// Derived values
	const selectedNode = selectedNodeId ? (nodes.find((n) => n.id === selectedNodeId) ?? null) : null;
	const selectedEdge = selectedLinkId ? (edges.find((e) => e.id === selectedLinkId) as Edge<InterfaceLabelEdgeData> | undefined ?? null) : null;
	const sidebarVisible = selectedNode !== null || selectedEdge !== null;
	const sidebarWidth = sidebarVisible && !sidebarCollapsed ? 320 : 0;
	const terminalPanelHeight = getTerminalPanelHeight(terminalPanelState, terminalBodyHeight);

	const activeTab = terminalTabs.find((t) => t.tabId === activeTabId) ?? null;
	const activeTabNodeId = activeTab?.nodeId ?? null;
	const activeTabNode = activeTabNodeId ? (nodes.find((n) => n.id === activeTabNodeId) ?? null) : null;
	const activeTabNodeStatus = activeTabNode?.data.status ?? "idle";
	const terminalStatus: TerminalStatus = (() => {
		if (!activeTab) return "disconnected";
		if (lastCommand?.status === "executing") return "busy";
		if (activeTabNode?.data.isBusy || busyNodeIds.has(activeTab.nodeId)) return "busy";
		// Keep indicator resilient if backend returns new non-idle status tokens.
		if (activeTabNodeStatus !== "idle" && activeTabNodeStatus !== "error") return "connected";
		return "disconnected";
	})();

	// Sidebar recent commands: history of the terminal tab for the selected node
	const sidebarRecentCommands = (() => {
		if (!selectedNode) return [];
		const tab = terminalTabs.find((t) => t.nodeId === selectedNode.id);
		return tab?.history ?? [];
	})();

	useEffect(() => {
		if (!isCreateNodeMenuOpen) return;
		function onOutsideClick(event: MouseEvent) {
			const target = event.target;
			if (!(target instanceof globalThis.Node)) return;
			if (createNodeMenuRef.current?.contains(target)) return;
			setIsCreateNodeMenuOpen(false);
		}
		document.addEventListener("mousedown", onOutsideClick);
		return () => document.removeEventListener("mousedown", onOutsideClick);
	}, [isCreateNodeMenuOpen]);

	const startTerminalResize = useCallback((event: ReactMouseEvent<HTMLDivElement>) => {
		event.preventDefault();
		setIsTerminalResizing(true);
		if (terminalPanelState === "hidden") {
			setTerminalPanelState("normal");
		}
		const startY = event.clientY;
		const startHeight = terminalPanelState === "hidden" ? 0 : terminalBodyHeight;
		function onMove(moveEvent: MouseEvent) {
			const delta = startY - moveEvent.clientY;
			const maxBodyHeight = Math.max(0, window.innerHeight - HEADER_HEIGHT - TERMINAL_HEADER_HEIGHT - TERMINAL_RESIZE_HANDLE_HEIGHT);
			const next = Math.max(0, Math.min(maxBodyHeight, startHeight + delta));
			if (next <= 0) {
				setTerminalBodyHeight(0);
				setTerminalPanelState("hidden");
			} else {
				setTerminalBodyHeight(next);
				setTerminalPanelState("normal");
			}
		}
		function onUp() {
			setIsTerminalResizing(false);
			window.removeEventListener("mousemove", onMove);
			window.removeEventListener("mouseup", onUp);
		}
		window.addEventListener("mousemove", onMove);
		window.addEventListener("mouseup", onUp);
	}, [terminalBodyHeight, terminalPanelState]);

	const renameNode = useCallback((nodeId: string, displayName: string) => {
		const nextName = displayName.trim();
		setNodeNames((curr) => ({ ...curr, [nodeId]: nextName }));
		setNodes((curr) => curr.map((n) => n.id === nodeId ? { ...n, data: { ...n.data, displayName: nextName } } : n));
		void updateNodeName(baseUrl, nodeId, nextName).catch((err: unknown) => {
			setStatus((err instanceof Error ? err.message : String(err)) || "Failed to update node name");
		});
	}, [baseUrl]);

	const tabLabel = useCallback((tab: TerminalTab) => {
		return nodeNames[tab.nodeId] ?? tab.nodeId;
	}, [nodeNames]);

	return (
		<div className="relative h-screen w-screen bg-zinc-100 text-zinc-900">
			<header className="fixed left-0 top-0 z-[2000] flex w-full items-center justify-between gap-3 border-b border-zinc-200 bg-white px-4 py-3 relative">
				<input
					ref={importInputRef}
					type="file"
					accept="application/json"
					onChange={(e) => { void loadTopologyFromFile(e); }}
					className="hidden"
				/>
				<div className="flex items-center gap-3">
					<button type="button" onClick={() => void saveTopologyToFile()} disabled={busy || nodes.length === 0} className="flex items-center gap-2 rounded border border-zinc-700 px-3 py-1 text-sm hover:bg-zinc-100 disabled:opacity-60"><Save className="h-4 w-4" />Save</button>
					<button type="button" onClick={openImportPicker} disabled={busy} className="flex items-center gap-2 rounded border border-zinc-700 px-3 py-1 text-sm hover:bg-zinc-100 disabled:opacity-60"><FileUp className="h-4 w-4" />Load</button>
					<button type="button" onClick={requestClearTopology} disabled={busy} className="flex items-center gap-2 rounded border border-zinc-700 px-3 py-1 text-sm hover:bg-zinc-100 disabled:opacity-60"><RotateCcw className="h-4 w-4" />Clear</button>
					<div className="relative" ref={createNodeMenuRef}>
					<button
						type="button"
						onClick={() => setIsCreateNodeMenuOpen((v) => !v)}
						disabled={busy}
						className="flex items-center gap-2 rounded border border-zinc-700 px-3 py-1 text-sm hover:bg-zinc-100 disabled:opacity-60"
					>
						<Network className="h-4 w-4" />
						Create node
					</button>
					{isCreateNodeMenuOpen ? (
						<div className="absolute left-0 top-full z-30 mt-2 flex w-full flex-col rounded border border-zinc-300 bg-white p-1 shadow-lg">
							<button type="button" onClick={() => void createNode("host")} className="flex items-center gap-2 rounded px-3 py-2 text-left text-sm hover:bg-zinc-100"><Monitor className="h-4 w-4 text-gray-600" />Host</button>
							<button type="button" onClick={() => void createNode("switch")} className="flex items-center gap-2 rounded px-3 py-2 text-left text-sm hover:bg-zinc-100"><Network className="h-4 w-4 text-gray-600" />Switch</button>
							<button type="button" onClick={() => void createNode("router")} className="flex items-center gap-2 rounded px-3 py-2 text-left text-sm hover:bg-zinc-100"><Router className="h-4 w-4 text-gray-600" />Router</button>
						</div>
					) : null}
					</div>
				</div>
				<div className="pointer-events-none absolute left-1/2 top-1/2 -translate-x-1/2 -translate-y-1/2 text-sm text-zinc-600">{nodes.length} nodes, {edges.length} links</div>
				<button type="button" onClick={() => window.open("/help", "_blank")} className="flex items-center gap-2 rounded border border-zinc-700 px-3 py-1 text-sm hover:bg-zinc-100"><CircleHelp className="h-4 w-4" />Help</button>
			</header>

			{/* Right sidebar */}
			<Sidebar
				selectedNode={selectedNode}
				selectedEdge={selectedEdge}
				nodes={nodes}
				edges={edges as Edge<InterfaceLabelEdgeData>[]}
				isBusy={busy}
				isCollapsed={sidebarCollapsed}
				recentCommands={sidebarRecentCommands}
				lastCommand={lastCommand}
				onOpenTerminal={openTerminalForNode}
				onToggleRun={(nodeId) => void toggleNodeRun(nodeId)}
				onToggleFreeze={(nodeId) => void toggleFreezeNode(nodeId)}
				onRequestDeleteNode={requestDeleteSelectedNode}
				onDeleteLink={() => void deleteSelectedLink()}
				onToggleCollapse={() => {
					setSelectedNodeId("");
					setSelectedLinkId("");
					setSidebarCollapsed(true);
					setNodes((curr) => applySelectedNode(curr, ""));
				}}
				onRenameNode={renameNode}
			/>

			{/* Terminal panel */}
			<TerminalPanel
				tabs={terminalTabs}
				activeTabId={activeTabId}
				getTabLabel={tabLabel}
				panelState={terminalPanelState}
				terminalStatus={terminalStatus}
				terminalNodeState={activeTabNodeStatus}
				sidebarWidth={sidebarWidth}
				onTabChange={setActiveTabId}
				onTabClose={closeTerminalTab}
				onPanelStateChange={setTerminalPanelState}
				onInputChange={handleTabInputChange}
				onHistoryNavigate={handleTabHistoryNavigate}
				onSubmit={(tabId) => void submitTerminalTab(tabId)}
				normalBodyHeight={terminalBodyHeight}
				onStartResize={startTerminalResize}
				isResizing={isTerminalResizing}
			/>

			{/* Main canvas */}
			<main
				className="absolute left-0 top-14"
				style={{
					bottom: terminalPanelState === "fullscreen" ? 0 : terminalPanelHeight,
					right: sidebarWidth,
					transition: "right 200ms ease-in-out",
				}}
			>
				{/* Confirm modal */}
				{confirmAction ? (
					<div className="fixed left-1/2 top-1/2 z-30 w-[360px] -translate-x-1/2 -translate-y-1/2 rounded border border-zinc-300 bg-white p-4 shadow-lg">
						<div className="mb-3 text-sm font-semibold text-zinc-900">
							{confirmAction === "delete-node" ? "Delete node?" : "Clear topology?"}
						</div>
						<div className="mb-4 text-sm text-zinc-700">This action cannot be undone.</div>
						<div className="flex justify-end gap-2">
							<button type="button" onClick={() => setConfirmAction(null)} disabled={busy} className="rounded border border-zinc-300 px-3 py-1 text-sm hover:bg-zinc-100">Cancel</button>
							<button
								type="button"
								onClick={() => { confirmAction === "delete-node" ? void deleteSelectedNode() : void clearCurrentTopology(); }}
								disabled={busy}
								className="rounded border border-zinc-700 px-3 py-1 text-sm hover:bg-zinc-100 disabled:opacity-60"
							>
								Confirm
							</button>
						</div>
					</div>
				) : null}

				{/* Interface selection modal */}
				{pendingConnection ? (
					<div className="fixed left-1/2 top-1/2 z-30 w-[360px] -translate-x-1/2 -translate-y-1/2 rounded border border-zinc-300 bg-white p-4 shadow-lg">
						<div className="mb-3 text-sm font-semibold text-zinc-900">Choose interfaces</div>
						<div className="mb-3 flex flex-col gap-1">
							<label className="text-xs text-zinc-600" htmlFor="source-interface">Source interface</label>
							<select id="source-interface" value={pendingConnection.sourceInterfaceID} onChange={(e) => updatePendingConnection("sourceInterfaceID", e.target.value)} className="rounded border border-zinc-300 px-2 py-1 text-sm">
								{sourceInterfaceOptions.map((iface) => <option key={iface.id} value={iface.id}>{iface.name}</option>)}
							</select>
						</div>
						<div className="mb-4 flex flex-col gap-1">
							<label className="text-xs text-zinc-600" htmlFor="target-interface">Target interface</label>
							<select id="target-interface" value={pendingConnection.targetInterfaceID} onChange={(e) => updatePendingConnection("targetInterfaceID", e.target.value)} className="rounded border border-zinc-300 px-2 py-1 text-sm">
								{targetInterfaceOptions.map((iface) => <option key={iface.id} value={iface.id}>{iface.name}</option>)}
							</select>
						</div>
						<div className="flex justify-end gap-2">
							<button type="button" onClick={() => { setPendingConnection(null); setConnectionSourceNodeId(""); }} className="rounded border border-zinc-300 px-3 py-1 text-sm hover:bg-zinc-100">Cancel</button>
							<button type="button" onClick={() => void confirmPendingConnection()} disabled={busy} className="rounded border border-zinc-700 px-3 py-1 text-sm hover:bg-zinc-100 disabled:opacity-60">Create link</button>
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
					onConnect={(connection) => { void onConnect(connection); }}
					onConnectStart={(_, params) => {
						setSelectedLinkId("");
						setConnectionSourceNodeId(params.nodeId ?? "");
					}}
					onConnectEnd={() => { if (!pendingConnectionRef.current) setConnectionSourceNodeId(""); }}
					onNodeClick={(_, node) => {
						setSelectedNodeId(node.id);
						setSelectedLinkId("");
						setSidebarCollapsed(false);
						setNodes((curr) => applySelectedNode(curr, node.id));
					}}
					onEdgeClick={((_, edge) => {
						setSelectedNodeId("");
						setSelectedLinkId(edge.id);
						setSidebarCollapsed(false);
						setNodes((curr) => applySelectedNode(curr, ""));
					}) as EdgeMouseHandler}
					onNodeDragStop={(_, node) => {
						void persistNodePosition(node.id, { x: node.position.x, y: node.position.y });
					}}
					onPaneClick={() => {
						setIsCreateNodeMenuOpen(false);
					}}
					zoomOnScroll
					elevateNodesOnSelect={false}
					connectionLineType={ConnectionLineType.Straight}
					defaultEdgeOptions={{ type: "interfaceLabel", style: EDGE_STYLE }}
					nodesConnectable
					fitView
					defaultViewport={{ x: 0, y: 0, zoom: 1 }}
					proOptions={{ hideAttribution: true }}
				>
					<MiniMap zoomable pannable nodeStrokeWidth={3} nodeColor="#e5e7eb" />
					<Controls />
					<Background gap={18} size={1} />
				</ReactFlow>
			</main>
		</div >
	);
}
