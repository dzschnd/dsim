import { ChevronDown, Loader2, Maximize2 } from "lucide-react";
import { createPortal } from "react-dom";
import { useEffect, useRef, useState, type MouseEvent as ReactMouseEvent } from "react";

export type TerminalTab = {
	tabId: number;
	nodeId: string;
	lines: string[];
	input: string;
	history: string[];
	historyIndex: number | null;
	historyDraft: string | null;
};

export type TerminalPanelState = "hidden" | "normal" | "fullscreen";

export type TerminalStatus = "connected" | "disconnected" | "busy" | "idle";

const HEADER_HEIGHT = 44;

export function getTerminalPanelHeight(state: TerminalPanelState, normalBodyHeight: number): number {
	if (state === "hidden") return HEADER_HEIGHT;
	if (state === "normal") return HEADER_HEIGHT + normalBodyHeight;
	return 0;
}

type TerminalPanelProps = {
	tabs: TerminalTab[];
	activeTabId: number | null;
	getTabLabel: (tab: TerminalTab) => string;
	panelState: TerminalPanelState;
	terminalStatus: TerminalStatus;
	terminalNodeState: string | null;
	sidebarWidth: number;
	normalBodyHeight: number;
	onTabChange: (tabId: number) => void;
	onTabClose: (tabId: number) => void;
	onPanelStateChange: (state: TerminalPanelState) => void;
	onInputChange: (tabId: number, value: string) => void;
	onHistoryNavigate: (tabId: number, direction: "up" | "down") => void;
	onSubmit: (tabId: number) => void;
	onStartResize: (event: ReactMouseEvent<HTMLDivElement>) => void;
	isResizing: boolean;
};

function StatusIndicator({ status }: { status: TerminalStatus }) {
	const busy = status === "busy";
	const disconnected = status === "disconnected";
	return (
		<div className="flex h-3.5 w-3.5 items-center justify-center">
			{busy ? (
				<Loader2 className="h-3.5 w-3.5 animate-spin text-yellow-500" />
			) : (
				<div className={`h-2 w-2 rounded-full ${disconnected ? "bg-gray-400" : "bg-green-500"}`} />
			)}
		</div>
	);
}

function TerminalBody({
	tab,
	allowInput,
	inputPlaceholder,
	onInputChange,
	onHistoryNavigate,
	onSubmit,
}: {
	tab: TerminalTab;
	allowInput: boolean;
	inputPlaceholder: string;
	onInputChange: (value: string) => void;
	onHistoryNavigate: (direction: "up" | "down") => void;
	onSubmit: () => void;
}) {
	const scrollRef = useRef<HTMLDivElement | null>(null);
	const [localInput, setLocalInput] = useState(tab.input);

	useEffect(() => {
		setLocalInput(tab.input);
	}, [tab.input]);

	useEffect(() => {
		if (!scrollRef.current) return;
		scrollRef.current.scrollTop = scrollRef.current.scrollHeight;
	}, [tab.lines]);

	return (
		<div className="flex min-h-0 flex-1 flex-col">
			<div ref={scrollRef} className="node-terminal-scroll flex-1 overflow-y-auto p-4 font-mono text-sm text-gray-200">
				{tab.lines.map((line, i) => (
					<div key={i} className="whitespace-pre-wrap break-words leading-5 select-text">{line}</div>
				))}
			</div>
			<div className="border-t border-gray-600 p-4">
				<div className="flex items-center gap-2">
					{allowInput && <span className="shrink-0 font-mono text-sm text-green-400">$</span>}
					<input
						type="text"
						value={localInput}
						onChange={(e) => {
							setLocalInput(e.target.value);
							onInputChange(e.target.value);
						}}
						onKeyDown={(e) => {
							if (!allowInput) return;
							if (e.key === "Enter") {
								e.preventDefault();
								onSubmit();
								return;
							}
							if (e.key === "ArrowUp") {
								e.preventDefault();
								onHistoryNavigate("up");
								return;
							}
							if (e.key === "ArrowDown") {
								e.preventDefault();
								onHistoryNavigate("down");
							}
						}}
						placeholder={inputPlaceholder}
						disabled={!allowInput}
						className={`flex-1 bg-transparent font-mono text-sm outline-none placeholder:text-gray-500 ${allowInput ? "text-gray-200" : "text-gray-500"
							}`}
						autoComplete="off"
						spellCheck={false}
					/>
				</div>
			</div>
		</div>
	);
}

export function TerminalPanel({
	tabs,
	activeTabId,
	getTabLabel,
	panelState,
	terminalStatus,
	terminalNodeState,
	sidebarWidth,
	normalBodyHeight,
	onTabChange,
	onTabClose,
	onPanelStateChange,
	onInputChange,
	onHistoryNavigate,
	onSubmit,
	onStartResize,
	isResizing,
}: TerminalPanelProps) {
	const activeTab = tabs.find((t) => t.tabId === activeTabId) ?? null;
	const isCollapsed = panelState === "hidden";
	const isFullscreen = panelState === "fullscreen";
	const isDisconnected = terminalStatus === "disconnected";
	const isFrozen = terminalNodeState === "frozen";
	const allowInput = !isDisconnected && !isFrozen;
	const inputPlaceholder = isDisconnected ? "start node to run commands" : isFrozen ? "unfreeze node to run commands" : "enter command";

	useEffect(() => {
		if (!isFullscreen) return;
		function onKeyDown(event: KeyboardEvent) {
			if (event.key === "Escape") onPanelStateChange("normal");
		}
		window.addEventListener("keydown", onKeyDown);
		return () => window.removeEventListener("keydown", onKeyDown);
	}, [isFullscreen, onPanelStateChange]);

	const header = (
		<div className="h-11 flex-shrink-0 border-b border-gray-600 px-4">
			<div className="flex h-full items-center justify-between gap-3">
				<div className="flex min-w-0 items-center gap-3">
					<StatusIndicator status={terminalStatus} />
					<div className="flex min-w-0 items-center gap-1">
						{tabs.map((tab) => (
							<div key={tab.tabId} className="flex items-center">
								<button
									type="button"
									onClick={() => onTabChange(tab.tabId)}
									className={`h-7 max-w-[120px] truncate rounded-l px-3 text-xs transition-colors ${tab.tabId === activeTabId ? "bg-gray-700 text-white" : "text-gray-400 hover:text-gray-200"
										}`}
								>
									{getTabLabel(tab)}
								</button>
								<button
									type="button"
									onClick={(e) => {
										e.stopPropagation();
										onTabClose(tab.tabId);
									}}
									className={`h-7 rounded-r px-1.5 text-xs transition-colors ${tab.tabId === activeTabId ? "bg-gray-700 text-gray-400 hover:text-gray-200" : "text-gray-600 hover:text-gray-400"
										}`}
									aria-label="Close tab"
								>
									×
								</button>
							</div>
						))}
					</div>
				</div>
				<div className="flex items-center gap-2">
					{!isFullscreen ? (
						<button
							type="button"
							onClick={() => onPanelStateChange(isCollapsed ? "normal" : "hidden")}
							className="flex h-6 w-6 items-center justify-center rounded transition-colors hover:bg-gray-700"
							aria-label={isCollapsed ? "Expand terminal" : "Collapse terminal"}
						>
							<ChevronDown className={`h-4 w-4 text-gray-400 transition-transform ${isCollapsed ? "rotate-180" : ""}`} />
						</button>
					) : null}
					<button
						type="button"
						onClick={() => onPanelStateChange(isFullscreen ? "normal" : "fullscreen")}
						className="flex h-6 w-6 items-center justify-center rounded transition-colors hover:bg-gray-700"
						aria-label={isFullscreen ? "Exit fullscreen" : "Fullscreen"}
					>
						<Maximize2 className="h-3.5 w-3.5 text-gray-400" />
					</button>
				</div>
			</div>
		</div>
	);

	const emptyBody = <div className="flex flex-1 items-center justify-center text-sm text-gray-500">Open a node terminal from the node or sidebar</div>;

	if (isFullscreen) {
		return createPortal(
			<div className="fixed inset-0 z-[5000] flex flex-col bg-[#1E293B]">
				{header}
				{activeTab ? (
					<TerminalBody
						tab={activeTab}
						allowInput={allowInput}
						inputPlaceholder={inputPlaceholder}
						onInputChange={(v) => onInputChange(activeTab.tabId, v)}
						onHistoryNavigate={(d) => onHistoryNavigate(activeTab.tabId, d)}
						onSubmit={() => onSubmit(activeTab.tabId)}
					/>
				) : emptyBody}
			</div>,
			document.body,
		);
	}

	const panelHeight = isCollapsed ? HEADER_HEIGHT : HEADER_HEIGHT + normalBodyHeight;

	return (
		<div
			className="fixed bottom-0 left-0 z-[500] flex flex-col overflow-hidden border-t border-gray-600 bg-[#1E293B]"
			style={{
				right: `${sidebarWidth}px`,
				height: `${panelHeight}px`,
				transition: isResizing ? "right 200ms ease-in-out" : "height 200ms ease-in-out, right 200ms ease-in-out",
			}}
		>
			<div
				onMouseDown={onStartResize}
				className="h-1 w-full cursor-row-resize bg-transparent hover:bg-gray-500/40"
				aria-label="Resize terminal"
			/>
			{header}
			{!isCollapsed && (activeTab ? (
				<TerminalBody
					tab={activeTab}
					allowInput={allowInput}
					inputPlaceholder={inputPlaceholder}
					onInputChange={(v) => onInputChange(activeTab.tabId, v)}
					onHistoryNavigate={(d) => onHistoryNavigate(activeTab.tabId, d)}
					onSubmit={() => onSubmit(activeTab.tabId)}
				/>
			) : emptyBody)}
		</div>
	);
}
