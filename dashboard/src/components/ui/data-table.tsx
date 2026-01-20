"use client"

import {
  ColumnDef,
  flexRender,
  getCoreRowModel,
  getSortedRowModel,
  SortingState,
  useReactTable,
  Row,
} from "@tanstack/react-table"
import { useState, ReactNode, Fragment, createContext, useContext } from "react"

import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"

// Context for row expansion
interface RowExpansionContextValue {
  expandedRowId: string | null
  toggleRowExpansion: (rowId: string) => void
}

const RowExpansionContext = createContext<RowExpansionContextValue>({
  expandedRowId: null,
  toggleRowExpansion: () => {},
})

export function useRowExpansion() {
  return useContext(RowExpansionContext)
}

interface DataTableProps<TData> {
  columns: ColumnDef<TData, any>[]
  data: TData[]
  renderExpandedRow?: (row: Row<TData>, onCollapse: () => void) => ReactNode
}

export function DataTable<TData>({
  columns,
  data,
  renderExpandedRow,
}: DataTableProps<TData>) {
  const [sorting, setSorting] = useState<SortingState>([])
  const [expandedRowId, setExpandedRowId] = useState<string | null>(null)

  const toggleRowExpansion = (rowId: string) => {
    setExpandedRowId(prev => prev === rowId ? null : rowId)
  }

  const table = useReactTable({
    data,
    columns,
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: getSortedRowModel(),
    onSortingChange: setSorting,
    state: {
      sorting,
    },
  })

  return (
    <RowExpansionContext.Provider value={{ expandedRowId, toggleRowExpansion }}>
      <div className="rounded-lg border border-border bg-card">
        <Table>
          <TableHeader>
            {table.getHeaderGroups().map((headerGroup) => (
              <TableRow key={headerGroup.id} className="bg-muted/50 hover:bg-muted/50">
                {headerGroup.headers.map((header) => (
                  <TableHead key={header.id}>
                    {header.isPlaceholder
                      ? null
                      : flexRender(
                          header.column.columnDef.header,
                          header.getContext()
                        )}
                  </TableHead>
                ))}
              </TableRow>
            ))}
          </TableHeader>
          <TableBody>
            {table.getRowModel().rows?.length ? (
              table.getRowModel().rows.map((row) => {
                const isExpanded = expandedRowId === row.id
                return (
                  <Fragment key={row.id}>
                    <TableRow
                      data-state={row.getIsSelected() && "selected"}
                      className={isExpanded ? "border-b-0" : ""}
                    >
                      {row.getVisibleCells().map((cell) => (
                        <TableCell key={cell.id}>
                          {flexRender(cell.column.columnDef.cell, cell.getContext())}
                        </TableCell>
                      ))}
                    </TableRow>
                    {isExpanded && renderExpandedRow && (
                      <tr className="border-b border-border">
                        <td colSpan={columns.length} className="p-0">
                          {renderExpandedRow(row, () => setExpandedRowId(null))}
                        </td>
                      </tr>
                    )}
                  </Fragment>
                )
              })
            ) : (
              <TableRow>
                <TableCell colSpan={columns.length} className="h-24 text-center text-muted-foreground">
                  No results.
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </div>
    </RowExpansionContext.Provider>
  )
}
