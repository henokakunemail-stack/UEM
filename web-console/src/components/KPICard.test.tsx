import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { KPICard } from './KPICard'

describe('KPICard', () => {
  it('renders an unknown value as unknown, never with a progress bar', () => {
    const { container } = render(
      <KPICard title="Offline" value="—" icon={null} variant="unknown" badge="Unknown" progress={undefined} />,
    )
    const card = container.querySelector('.kpi-card')
    expect(card?.classList.contains('unknown')).toBe(true)
    expect(card?.classList.contains('success')).toBe(false)
    expect(screen.getByText('—')).toBeTruthy()
    expect(container.querySelector('.kpi-progress-bar')).toBeNull()
  })

  it('shows the progress percentage from current and total', () => {
    render(<KPICard title="Online" value="3" icon={null} progress={{ current: 3, total: 4 }} />)
    expect(screen.getByText('75%')).toBeTruthy()
  })

  it('is a real button when clickable', () => {
    const onClick = vi.fn()
    render(<KPICard title="Devices" value={5} icon={null} onClick={onClick} />)
    fireEvent.click(screen.getByRole('button'))
    expect(onClick).toHaveBeenCalledOnce()
  })
})
