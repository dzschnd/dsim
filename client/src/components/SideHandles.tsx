import { useEffect } from "react";
import { Handle, Position, useUpdateNodeInternals } from "reactflow";

type SideHandlesProps = {
	currentNodeId: string;
	connectionSourceNodeId: string;
	nodeType: string;
};

export function SideHandles({ currentNodeId, connectionSourceNodeId, nodeType: _nodeType }: SideHandlesProps) {
	const updateNodeInternals = useUpdateNodeInternals();
	const isConnecting = connectionSourceNodeId !== "";
	const isSourceNode = isConnecting && connectionSourceNodeId === currentNodeId;
	const sharedClass =
		"!left-1/2 !right-auto !transform !h-[16px] !w-[16px] !-translate-x-1/2 !-translate-y-1/2 !rounded-full !border-2 !border-slate-700/70 !bg-white";
	const targetClass = isConnecting && !isSourceNode
		? `${sharedClass} !z-20 !cursor-pointer`
		: `${sharedClass} !z-10`;
	const sourceClass = isConnecting && !isSourceNode
		? `${sharedClass} !pointer-events-none !z-0`
		: `${sharedClass} !z-20`;
	const handleStyle = { top: "50%" };

	useEffect(() => {
		updateNodeInternals(currentNodeId);
	}, [currentNodeId, updateNodeInternals]);

	return (
		<>
			<Handle id="center-target" type="target" position={Position.Top} className={targetClass} style={handleStyle} />
			<Handle
				id="center-source"
				type="source"
				position={Position.Top}
				className={sourceClass}
				style={handleStyle}
				isConnectable={!isConnecting || isSourceNode}
			/>
		</>
	);
}
