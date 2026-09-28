import React, { Children, isValidElement } from 'react'

interface DataTableProps {
  /** A plain <table> element. Columns, rows and actions stay where they are. */
  children: React.ReactNode
  label?: string
  className?: string
}

// Table parts are plain DOM elements; the index signature keeps the original
// props (style, onClick, aria-*, data-*) intact while the clone adds its own.
type Props = {
  children?: React.ReactNode
  className?: string
  colSpan?: number
  [key: string]: unknown
}

/** Literal text of a header cell: nested elements still count, values do not. */
const textOf = (node: React.ReactNode): string => {
  if (typeof node === 'string' || typeof node === 'number') return String(node)
  if (Array.isArray(node)) return node.map(textOf).join(' ')
  if (isValidElement(node)) return textOf((node.props as Props).children)
  return ''
}

const findTable = (node: React.ReactNode): React.ReactElement<Props> | null => {
  let found: React.ReactElement<Props> | null = null
  Children.forEach(node, (child) => {
    if (found || !isValidElement(child)) return
    if (child.type === 'table') found = child as React.ReactElement<Props>
    else if (child.type === 'thead' || child.type === 'tbody' || child.type === 'tfoot') {
      // The call sites pass <thead>/<tbody> directly — every page does, because
      // the contract is "wrap a plain table", and writing a redundant <table>
      // element in 12 places is noise. So the parts, not the table, are what
      // actually arrive, and matching only 'table' sent every page down the
      // fallback branch: no <table> element, no cell labels, no card layout,
      // and no error anywhere. The missing element is synthesised below.
      found = {
        type: 'table',
        key: 'datatable-synthesised',
        props: { children: node },
      } as unknown as React.ReactElement<Props>
    } else found = findTable((child.props as Props).children)
  })
  return found
}

/** Column names in order, or [] when the header is a runtime value. */
const headerLabels = (table: React.ReactElement<Props>): string[] => {
  const head = Children.toArray(table.props.children).find(
    (c) => isValidElement(c) && c.type === 'thead'
  )
  if (!isValidElement(head)) return []
  const row = Children.toArray((head.props as Props).children).find(
    (c) => isValidElement(c) && c.type === 'tr'
  )
  if (!isValidElement(row)) return []
  return Children.toArray((row.props as Props).children)
    .filter((c) => isValidElement(c))
    .map((c) => textOf((c.props as Props).children).trim())
}

const decorateBody = (body: React.ReactNode, labels: string[]): React.ReactNode =>
  Children.map(body, (row) => {
    if (!isValidElement(row) || row.type !== 'tr') return row
    let column = 0
    return React.cloneElement(row as React.ReactElement<Props>, { role: 'row' }, [
      ...Children.toArray((row.props as Props).children).map((cell) => {
        if (!isValidElement(cell) || cell.type !== 'td') return cell
        const { children, ...rest } = cell.props as Props
        // A full-width cell (empty state, loading row) has no single column, so
        // it gets no label and the row is flagged for the card layout.
        if (rest.colSpan && rest.colSpan > 1) {
          column += rest.colSpan
          return <td {...rest} role="cell">{children}</td>
        }
        const dataLabel = labels[column] || undefined
        column += 1
        return <td {...rest} data-label={dataLabel} role="cell">{children}</td>
      }),
    ])
  })

/**
 * Wraps an existing table and labels its cells from the header row, so a
 * stylesheet can turn each row into a card below 768px without a second copy of
 * the row actions. The table keeps its own semantics; the roles are explicit
 * because a `display: block` card layout would otherwise drop them.
 */
export const DataTable: React.FC<DataTableProps> = ({ children, label, className }) => {
  const table = findTable(children)
  if (!table) return <div className="table-scroll">{children}</div>

  const labels = headerLabels(table)
  const { children: body, className: tableClass, ...rest } = table.props

  const sections = Children.toArray(body).map((section) => {
    if (!isValidElement(section)) return section
    if (section.type === 'thead')
      return React.cloneElement(section as React.ReactElement<Props>, { role: 'rowgroup' }, [
        ...Children.toArray((section.props as Props).children).map((row) =>
          isValidElement(row) && row.type === 'tr'
            ? React.cloneElement(row as React.ReactElement<Props>, { role: 'row' }, [
                ...Children.toArray((row.props as Props).children).map((cell) =>
                  isValidElement(cell) && cell.type === 'th'
                    ? React.cloneElement(cell as React.ReactElement<Props>, {
                        role: 'columnheader',
                      })
                    : cell
                ),
              ])
            : row
        ),
      ])
    if (section.type === 'tbody')
      return React.cloneElement(section as React.ReactElement<Props>, { role: 'rowgroup' }, [
        decorateBody((section.props as Props).children, labels),
      ])
    return section
  })

  return (
    // table-wrapper keeps today's overflow rule until the new sheet lands.
    <div className={`table-scroll table-wrapper${className ? ` ${className}` : ''}`}>
      {React.cloneElement(table, {
        ...rest,
        className: tableClass ? `${tableClass} data-table` : 'data-table',
        role: 'table',
        'aria-label': label,
        children: sections,
      })}
    </div>
  )
}
