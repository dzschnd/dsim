import { useEffect, useState, useRef } from "react";
import { createPortal } from "react-dom";

type NodeTerminalProps = {
	terminalLines: string[];
	terminalInput: string;
	isFullscreen: boolean;
	onInputChange: (value: string) => void;
	onHistoryNavigate: (direction: "up" | "down") => void;
	onSubmit: () => void;
	onToggleFullscreen: () => void;
};

export function NodeTerminal({
	terminalLines,
	terminalInput,
	isFullscreen,
	onInputChange,
	onHistoryNavigate,
	onSubmit,
	onToggleFullscreen,
}: NodeTerminalProps) {
	const scrollRef = useRef<HTMLDivElement | null>(null);
	const [inputValue, setInputValue] = useState(terminalInput);

	useEffect(() => {
		setInputValue(terminalInput);
	}, [terminalInput]);

	useEffect(() => {
		if (!scrollRef.current) {
			return;
		}
		scrollRef.current.scrollTop = scrollRef.current.scrollHeight;
	}, [terminalLines]);

	const terminalBody = (
		<>
			<button
				type="button"
				onClick={(event) => {
					event.stopPropagation();
					onToggleFullscreen();
				}}
				onPointerDown={(event) => {
					event.stopPropagation();
				}}
				className="nodrag nopan absolute right-2 top-2 z-10 flex h-7 w-7 items-center justify-center rounded border border-slate-700 bg-zinc-900/90 text-zinc-200 hover:bg-zinc-800"
				aria-label={isFullscreen ? "Exit full screen terminal" : "Full screen terminal"}
			>
				<svg viewBox="0 0 24 24" className="h-4 w-4" fill="none" stroke="currentColor" strokeWidth="1.8">
					{isFullscreen ? (
						<>
							<path d="M9 4H4v5" />
							<path d="M15 4h5v5" />
							<path d="M4 15v5h5" />
							<path d="M20 15v5h-5" />
						</>
					) : (
						<>
							<path d="M9 4H4v5" />
							<path d="M4 4l6 6" />
							<path d="M15 4h5v5" />
							<path d="M20 4l-6 6" />
							<path d="M4 20l6-6" />
							<path d="M4 15v5h5" />
							<path d="M20 20l-6-6" />
							<path d="M20 15v5h-5" />
						</>
					)}
				</svg>
			</button>
			<div
				ref={scrollRef}
				className="node-terminal-scroll nowheel flex-1 cursor-text overflow-y-auto px-3 pb-2 pt-2 select-text"
				onWheel={(event) => {
					event.stopPropagation();
				}}
			>
				{terminalLines.length > 0
					? terminalLines.map((line, index) => (
						<div
							key={`${line}-${index}`}
							className="select-text break-words whitespace-pre-wrap leading-5 text-zinc-300"
						>
							{line}
						</div>
					))
					: null}
			</div>
			<div className="flex items-center gap-2 border-t border-zinc-800 px-3 py-2">
				<span className="text-emerald-400">$</span>
				<input
					type="text"
					value={inputValue}
					onChange={(event) => {
						event.stopPropagation();
						setInputValue(event.target.value);
						onInputChange(event.target.value);
					}}
					onClick={(event) => {
						event.stopPropagation();
					}}
					onPointerDown={(event) => {
						event.stopPropagation();
					}}
					onKeyDown={(event) => {
						event.stopPropagation();
						if (event.key === "Enter") {
							event.preventDefault();
							onSubmit();
							return;
						}
						if (event.key === "ArrowUp") {
							event.preventDefault();
							onHistoryNavigate("up");
							return;
						}
						if (event.key === "ArrowDown") {
							event.preventDefault();
							onHistoryNavigate("down");
						}
					}}
					className="nodrag nopan w-full select-text border-none bg-transparent p-0 text-zinc-100 outline-none placeholder:text-zinc-600"
					placeholder="enter command"
				/>
			</div>
		</>
	);

	const inlineTerminal = (
		<div className="nodrag nopan nowheel absolute bottom-full left-1/2 z-[1000] mb-2 flex h-44 w-64 -translate-x-1/2 cursor-default flex-col overflow-hidden rounded border border-slate-800 bg-zinc-950 text-left font-mono text-[8px] text-zinc-100 shadow-lg">
			{terminalBody}
		</div>
	);

	const fullscreenTerminal = (
		<div className="fixed inset-0 z-[5000] flex h-screen w-screen cursor-default flex-col overflow-hidden bg-zinc-950 text-left font-mono text-[12px] text-zinc-100">
			{terminalBody}
		</div>
	);

	if (!isFullscreen) {
		return inlineTerminal;
	}

	return createPortal(fullscreenTerminal, document.body);
}
