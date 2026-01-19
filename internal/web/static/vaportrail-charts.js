/**
 * VaporTrail Charts Library
 * Shared charting utilities for latency visualization
 */
const VaporTrail = (function () {
    'use strict';

    // ============================================
    // TIME UTILITIES
    // ============================================

    /**
     * Convert a Date to local ISO string for datetime-local input
     */
    function toLocalISO(d) {
        const offset = d.getTimezoneOffset() * 60000;
        return new Date(d.getTime() - offset).toISOString().slice(0, 16);
    }

    /**
     * Standard time display formats for Chart.js time axis
     */
    const TIME_DISPLAY_FORMATS = {
        millisecond: 'MMM d HH:mm:ss.SSS',
        second: 'MMM d HH:mm:ss',
        minute: 'MMM d HH:mm',
        hour: 'MMM d HH:mm',
        day: 'MMM d',
        week: 'MMM d',
        month: 'MMM yyyy',
        year: 'yyyy'
    };

    // Debounce timer for zoom events (shared across all charts)
    let zoomDebounceTimer = null;

    // ============================================
    // DATA PROCESSING
    // ============================================

    /**
     * Calculate suggested Y-axis max from P95 values
     * Uses 95th percentile of P95 values with 10% headroom
     */
    function calculateSuggestedYMax(dataArray) {
        const allP95Values = [];
        for (const data of dataArray) {
            for (const d of data) {
                if (d.Percentiles && d.Percentiles.length === 21) {
                    const val = d.Percentiles[19] / 1e6;
                    if (val > 0) allP95Values.push(val);
                }
            }
        }
        if (allP95Values.length === 0) return undefined;
        allP95Values.sort((a, b) => a - b);
        return allP95Values[Math.floor(allP95Values.length * 0.95)] * 1.1;
    }

    /**
     * Transform API data to bar chart data format
     */
    function transformToBarData(data, options = {}) {
        return data.map(d => {
            const timeMs = new Date(d.Time).getTime();
            const windowMs = (d.WindowSeconds || 60) * 1000;
            // Percentiles array: index 0=P0, 1=P5, 2=P10, ... 19=P95, 20=P100 (in nanoseconds)
            const p5 = d.Percentiles && d.Percentiles.length === 21 ? d.Percentiles[1] : (d.P0 || d.MinNS);
            const p95 = d.Percentiles && d.Percentiles.length === 21 ? d.Percentiles[19] : (d.P100 || d.MaxNS);
            return {
                x: timeMs + windowMs / 2, // Shift right by half window to align start
                y: [p5 / 1000000, p95 / 1000000],
                originalTime: d.Time,
                originalData: d,  // Full original data for bar width calculation
                targetId: options.targetId
            };
        });
    }

    // ============================================
    // CHART PLUGINS
    // ============================================

    /**
     * Create timeout background plugin - draws red backgrounds based on timeout percentage
     */
    function createTimeoutBackgroundPlugin(data, datasetIndex = 0) {
        return {
            id: 'customCanvasBackgroundColor',
            beforeDraw: (chart) => {
                const { ctx, chartArea } = chart;
                ctx.save();
                data.forEach((d, i) => {
                    const total = d.ProbeCount + d.TimeoutCount;
                    if (total === 0) return;
                    const timeoutPct = d.TimeoutCount / total;

                    const meta = chart.getDatasetMeta(datasetIndex);
                    const bar = meta.data[i];
                    if (bar) {
                        const width = bar.width;
                        const x = bar.x - width / 2;
                        const gb = Math.floor(255 * (1 - timeoutPct));
                        ctx.fillStyle = `rgb(255, ${gb}, ${gb})`;
                        ctx.fillRect(x, chartArea.top, width, chartArea.bottom - chartArea.top);
                    }
                });
                ctx.restore();
            }
        };
    }

    /**
     * Create median line plugin - draws P50 line on each bar
     */
    function createMedianLinePlugin(data, options = {}) {
        const datasetIndex = options.datasetIndex || 0;
        const hue = options.hue;
        const pluginId = options.pluginId || 'medianLine';

        return {
            id: pluginId,
            afterDatasetsDraw: (chart) => {
                const meta = chart.getDatasetMeta(datasetIndex);
                if (!meta || meta.hidden) return;

                const { ctx, scales } = chart;

                ctx.save();
                if (hue !== undefined) {
                    ctx.strokeStyle = `hsla(${hue}, 100%, 30%, 0.9)`;
                } else {
                    ctx.strokeStyle = 'rgba(0, 0, 0, 0.9)';
                }
                ctx.lineWidth = 3;

                data.forEach((d, idx) => {
                    if (!d || d.ProbeCount === 0) return;
                    const bar = meta.data[idx];
                    if (!bar) return;

                    const medianVal = (d.P50 || 0) / 1000000;
                    const medianY = scales.y.getPixelForValue(medianVal);
                    const width = bar.width;
                    const x = bar.x - width / 2;

                    ctx.beginPath();
                    ctx.moveTo(x, medianY);
                    ctx.lineTo(x + width, medianY);
                    ctx.stroke();
                });
                ctx.restore();
            }
        };
    }

    /**
     * Create hover highlight plugin - green tint on hovered column
     */
    function createHoverHighlightPlugin() {
        return {
            id: 'hoverHighlight',
            afterDatasetsDraw: (chart) => {
                const activeElements = chart.getActiveElements();
                if (activeElements.length > 0) {
                    const { ctx, chartArea } = chart;
                    const activePoint = activeElements[0];
                    const x = activePoint.element.x;
                    const width = activePoint.element.width;

                    ctx.save();
                    ctx.fillStyle = 'rgba(0, 255, 0, 0.2)';
                    ctx.fillRect(x - width / 2, chartArea.top, width, chartArea.bottom - chartArea.top);
                    ctx.restore();
                }
            }
        };
    }

    /**
     * Create bar width fix plugin - ensures bars have exact pixel widths based on time window
     * This prevents gaps when zoomed in and overlap when zoomed out
     */
    function createBarWidthFixPlugin() {
        return {
            id: 'barWidthFix',
            beforeDatasetsDraw: (chart) => {
                const xScale = chart.scales.x;
                if (!xScale) return;

                chart.data.datasets.forEach((dataset, datasetIndex) => {
                    if (chart.config.type !== 'bar') return;
                    const meta = chart.getDatasetMeta(datasetIndex);
                    if (meta.type !== 'bar') return;

                    meta.data.forEach((bar, index) => {
                        const dataPoint = dataset.data[index];
                        if (!dataPoint || !dataPoint.originalData) return;

                        const windowMs = (dataPoint.originalData.WindowSeconds || 60) * 1000;
                        const centerTime = dataPoint.x;
                        const startTime = centerTime - windowMs / 2;
                        const endTime = centerTime + windowMs / 2;

                        const startPixel = xScale.getPixelForValue(startTime);
                        const endPixel = xScale.getPixelForValue(endTime);
                        const exactWidth = Math.abs(endPixel - startPixel);

                        // Set the exact width on the bar element
                        bar.width = exactWidth;
                    });
                });
            }
        };
    }

    // ============================================
    // GRADIENT BUILDERS
    // ============================================

    /**
     * Create heatmap gradient for a bar
     * @param {CanvasRenderingContext2D} ctx - Canvas context
     * @param {Object} scales - Chart scales
     * @param {Object} d - Data point with Percentiles array
     * @param {Object} options - { hue: number (optional), alpha: number (optional) }
     */
    function createHeatmapGradient(ctx, scales, d, options = {}) {
        const hue = options.hue;
        const alpha = options.alpha || 1.0;

        if (!d.Percentiles || d.Percentiles.length !== 21) {
            // Fallback for missing percentiles
            if (hue !== undefined) {
                return `hsla(${hue}, 85%, 45%, ${alpha})`;
            }
            return 'black';
        }

        const pValues = d.Percentiles.map(v => v / 1000000);
        const p5Val = pValues[1];
        const p95Val = pValues[19];
        const topY = scales.y.getPixelForValue(p95Val);
        const bottomY = scales.y.getPixelForValue(p5Val);

        if (Math.abs(topY - bottomY) < 0.1) {
            if (hue !== undefined) {
                return `hsla(${hue}, 90%, 40%, ${alpha})`;
            }
            return 'rgba(0,0,0,1)';
        }

        const gradient = ctx.createLinearGradient(0, topY, 0, bottomY);

        // Build gradient stops
        let stops = [];
        for (let i = 1; i <= 19; i++) {
            const val = pValues[i];
            const y = scales.y.getPixelForValue(val);
            let stop = (y - topY) / (bottomY - topY);
            stop = Math.max(0, Math.min(1, stop));

            // Distance from median (i=10) with exponential falloff
            const dist = Math.abs(i - 10) / 9;
            const curve = Math.pow(dist, 0.7);
            const lightness = 25 + curve * 65; // 25% (dark) to 90% (light)
            const stopAlpha = alpha * (1 - curve * 0.5);
            stops.push({ stop, lightness, alpha: stopAlpha });
        }

        // Sort and deduplicate stops
        stops.sort((a, b) => a.stop - b.stop);
        let uniqueStops = [];
        if (stops.length > 0) {
            let current = stops[0];
            for (let k = 1; k < stops.length; k++) {
                const next = stops[k];
                if (Math.abs(next.stop - current.stop) < 1e-5) {
                    if (next.lightness < current.lightness) {
                        current = next;
                    }
                } else {
                    uniqueStops.push(current);
                    current = next;
                }
            }
            uniqueStops.push(current);
        }

        // Add color stops
        uniqueStops.forEach(s => {
            if (hue !== undefined) {
                gradient.addColorStop(s.stop, `hsla(${hue}, 85%, ${s.lightness}%, ${s.alpha})`);
            } else {
                const gray = Math.floor(255 * (s.lightness / 100));
                gradient.addColorStop(s.stop, `rgba(${gray}, ${gray}, ${gray}, ${s.alpha})`);
            }
        });

        return gradient;
    }

    // ============================================
    // EXTERNAL TOOLTIP
    // ============================================

    /**
     * Create external tooltip handler for detailed percentile display
     */
    function createExternalTooltipHandler(tooltipEl, data) {
        return function (context) {
            const tooltipModel = context.tooltip;
            if (tooltipModel.opacity === 0) {
                tooltipEl.style.display = 'none';
                return;
            }

            // Smart relative positioning (5% offset, prefer left)
            const offset = context.chart.width * 0.05;
            const tooltipWidth = tooltipEl.offsetWidth;
            const chartLeft = context.chart.chartArea.left;
            const caretX = tooltipModel.caretX;

            let targetLeft = caretX - offset - tooltipWidth;

            if (targetLeft < chartLeft) {
                tooltipEl.style.left = (caretX + offset) + 'px';
                tooltipEl.style.right = 'auto';
            } else {
                tooltipEl.style.left = targetLeft + 'px';
                tooltipEl.style.right = 'auto';
            }

            // Build tooltip content
            if (tooltipModel.body) {
                const item = tooltipModel.dataPoints[0];
                const barData = context.chart.data.datasets[item.datasetIndex].data[item.dataIndex];
                const originalData = data[item.dataIndex];

                if (!originalData) return;

                let content = '';
                if (barData.originalTime) {
                    content += `<div style="font-weight:bold; margin-bottom:5px;">${new Date(barData.originalTime).toLocaleString()}</div>`;
                }

                if (originalData.Percentiles && originalData.Percentiles.length === 21) {
                    for (let k = 10; k >= 0; k--) {
                        const p = k * 10;
                        const idx = k * 2;
                        const val = originalData.Percentiles[idx] / 1e6;
                        content += `<div>P${p}: ${val.toFixed(2)} ms</div>`;
                    }
                } else {
                    content += `<div>Max: ${(originalData.P100 / 1e6).toFixed(2)} ms</div>`;
                    content += `<div>Median: ${(originalData.P50 / 1e6).toFixed(2)} ms</div>`;
                    content += `<div>Min: ${(originalData.P0 / 1e6).toFixed(2)} ms</div>`;
                }
                content += `<hr style="border: 0; border-top: 1px solid #555; margin: 5px 0;">`;
                content += `<div>Success: ${originalData.ProbeCount}</div>`;
                content += `<div>Timeout: ${originalData.TimeoutCount}</div>`;

                tooltipEl.innerHTML = content;
            }

            tooltipEl.style.display = 'block';
        };
    }

    // ============================================
    // MAIN CHART RENDERING
    // ============================================

    /**
     * Render a latency chart
     * @param {Object} options
     * @param {HTMLCanvasElement} options.canvas - The canvas element
     * @param {Array} options.data - Array of data objects or array of { targetId, data } for multi-target
     * @param {Object} options.range - { start: Date, end: Date }
     * @param {string} options.mode - 'heatmap' or 'line'
     * @param {boolean} options.useLogScale - Use logarithmic Y-axis
     * @param {Array} options.rawData - Optional raw data for scatter overlay
     * @param {HTMLElement} options.tooltipEl - Optional external tooltip element
     * @param {Object} options.targetsMap - Optional map of targetId to name
     * @param {Function} options.onZoomComplete - Optional callback(start, end) on zoom
     * @param {boolean} options.multiTarget - If true, data is array of { targetId, data }
     * @returns {Chart} The Chart.js instance
     */
    function renderLatencyChart(options) {
        const {
            canvas,
            data,
            range,
            mode = 'heatmap',
            useLogScale = false,
            rawData = [],
            tooltipEl = null,
            targetsMap = {},
            onZoomComplete = null,
            multiTarget = false
        } = options;

        const ctx = canvas.getContext('2d');

        if (mode === 'heatmap') {
            return renderHeatmapChart(ctx, {
                data,
                range,
                useLogScale,
                rawData,
                tooltipEl,
                targetsMap,
                onZoomComplete,
                multiTarget
            });
        } else {
            return renderLineChart(ctx, {
                data,
                range,
                rawData,
                targetsMap,
                onZoomComplete,
                multiTarget
            });
        }
    }

    function renderHeatmapChart(chartCtx, options) {
        const {
            data,
            range,
            useLogScale,
            rawData,
            tooltipEl,
            targetsMap,
            onZoomComplete,
            multiTarget
        } = options;

        const datasets = [];
        const plugins = [];

        if (multiTarget) {
            // Multi-target mode (dashboard view)
            for (let i = 0; i < data.length; i++) {
                const { targetId, data: targetData } = data[i];
                const hue = (i * 137) % 360;
                const targetName = targetsMap[targetId] || `Target ${targetId}`;

                const barData = transformToBarData(targetData, { targetId });

                datasets.push({
                    label: targetName,
                    data: barData,
                    backgroundColor: function (context) {
                        const chart = context.chart;
                        const { ctx, chartArea, scales } = chart;
                        if (!chartArea) return null;

                        const index = context.dataIndex;
                        const d = targetData[index];
                        if (!d || d.ProbeCount === 0) return 'rgba(0,0,0,0)';

                        return createHeatmapGradient(ctx, scales, d, { hue, alpha: 0.7 });
                    },
                    borderWidth: 0,
                    barPercentage: 1.0,
                    categoryPercentage: 1.0,
                    xAxisID: 'x',
                    grouped: false,
                    order: 10 - i
                });

                plugins.push(createMedianLinePlugin(targetData, {
                    datasetIndex: i,
                    hue,
                    pluginId: `medianLine_${i}_${targetId}`
                }));

                // Add bar width fix plugin once (for first dataset)
                if (i === 0) {
                    plugins.push(createBarWidthFixPlugin());
                }

                if (i === 0) {
                    plugins.push(createTimeoutBackgroundPlugin(targetData, 0));
                }
            }
        } else {
            // Single-target mode
            const barData = transformToBarData(data);
            const suggestedYMax = calculateSuggestedYMax([data]);

            datasets.push({
                label: 'Latency Range',
                data: barData,
                backgroundColor: function (context) {
                    const chart = context.chart;
                    const { ctx, chartArea, scales } = chart;
                    if (!chartArea) return null;

                    const index = context.dataIndex;
                    const d = data[index];
                    if (!d || d.ProbeCount === 0) return 'rgba(0,0,0,0)';

                    return createHeatmapGradient(ctx, scales, d);
                },
                borderWidth: 0,
                barPercentage: 1.0,
                categoryPercentage: 1.0,
                xAxisID: 'x',
                grouped: false,
                order: 10
            });

            // Add raw data scatter if available
            if (rawData && rawData.length > 0) {
                const scatterData = rawData.map(d => ({ x: d.Time, y: d.MinNS / 1000000 }));
                datasets.push({
                    label: 'Raw Latency (ms)',
                    data: scatterData,
                    type: 'scatter',
                    backgroundColor: '#00ffff',
                    borderColor: '#00ffff',
                    borderWidth: 2,
                    pointRadius: 2,
                    xAxisID: 'xRaw',
                    grouped: false,
                    order: 1
                });
            }

            plugins.push(createTimeoutBackgroundPlugin(data));
            plugins.push(createHoverHighlightPlugin());
            plugins.push(createMedianLinePlugin(data));
            plugins.push(createBarWidthFixPlugin());
        }

        // Calculate Y-axis max
        const dataArrays = multiTarget ? data.map(d => d.data) : [data];
        const suggestedYMax = calculateSuggestedYMax(dataArrays);

        // Build chart options
        const chartOptions = {
            responsive: true,
            maintainAspectRatio: false,
            interaction: { mode: 'index', intersect: false },
            scales: {
                x: {
                    type: 'time',
                    time: { displayFormats: TIME_DISPLAY_FORMATS },
                    grid: { display: false },
                    min: range.start.toISOString(),
                    max: range.end.toISOString(),
                    offset: false,
                    ticks: { maxRotation: 45, minRotation: 0 }
                },
                y: {
                    type: useLogScale ? 'logarithmic' : 'linear',
                    display: true,
                    title: { display: true, text: 'Latency (ms)' },
                    min: useLogScale ? undefined : 0,
                    max: useLogScale ? undefined : suggestedYMax
                }
            },
            plugins: {
                zoom: {
                    pan: {
                        enabled: true,
                        mode: 'x',
                        onPanComplete: function ({ chart }) {
                            if (onZoomComplete) {
                                const { min, max } = chart.scales.x;
                                onZoomComplete(new Date(min), new Date(max));
                            }
                        }
                    },
                    zoom: {
                        wheel: {
                            enabled: true,
                            speed: 0.1
                        },
                        drag: {
                            enabled: true,
                            modifierKey: 'shift'
                        },
                        mode: 'x',
                        onZoomComplete: function ({ chart }) {
                            // Debounce zoom callback to avoid rapid reloads during wheel zoom
                            clearTimeout(zoomDebounceTimer);
                            zoomDebounceTimer = setTimeout(function () {
                                if (onZoomComplete) {
                                    const { min, max } = chart.scales.x;
                                    onZoomComplete(new Date(min), new Date(max));
                                }
                            }, 1000);
                        }
                    }
                },
                legend: multiTarget ? {
                    display: true,
                    position: 'bottom',
                    labels: {
                        generateLabels: function (chart) {
                            return chart.data.datasets.map((ds, i) => ({
                                text: ds.label,
                                fillStyle: `hsla(${(i * 137) % 360}, 80%, 50%, 0.8)`,
                                strokeStyle: `hsla(${(i * 137) % 360}, 80%, 40%, 1)`,
                                lineWidth: 1,
                                hidden: false,
                                index: i
                            }));
                        }
                    }
                } : { display: false }
            }
        };

        // Add xRaw axis if raw data exists
        if (!multiTarget && rawData && rawData.length > 0) {
            chartOptions.scales.xRaw = {
                type: 'time',
                display: false,
                min: range.start.toISOString(),
                max: range.end.toISOString(),
                offset: false
            };
        }

        // Add tooltip configuration
        if (tooltipEl && !multiTarget) {
            chartOptions.plugins.tooltip = {
                enabled: false,
                external: createExternalTooltipHandler(tooltipEl, data)
            };
        } else if (multiTarget) {
            chartOptions.plugins.tooltip = {
                callbacks: {
                    label: function (context) {
                        const d = context.raw.originalData;
                        const tid = context.raw.targetId;
                        const tname = targetsMap[tid] || `Target ${tid}`;
                        if (!d) return '';
                        return [
                            `${tname}:`,
                            `  Max: ${(d.P100 / 1e6).toFixed(2)} ms`,
                            `  Median: ${(d.P50 / 1e6).toFixed(2)} ms`,
                            `  Min: ${(d.P0 / 1e6).toFixed(2)} ms`,
                            `  Success: ${d.ProbeCount}`,
                            `  Timeout: ${d.TimeoutCount}`
                        ];
                    }
                }
            };
        }

        return new Chart(chartCtx, {
            type: 'bar',
            data: { datasets },
            options: chartOptions,
            plugins
        });
    }

    function renderLineChart(chartCtx, options) {
        const {
            data,
            range,
            rawData,
            targetsMap,
            onZoomComplete,
            multiTarget
        } = options;

        const datasets = [];

        if (multiTarget) {
            const colors = ['#FF0000', '#00FF00', '#0000FF', '#FF00FF', '#00FFFF', '#FFFF00'];
            for (let i = 0; i < data.length; i++) {
                const { targetId, data: targetData } = data[i];
                const color = colors[i % colors.length];
                const targetName = targetsMap[targetId] || `Target ${targetId}`;

                const p50Data = targetData.map(d => ({ x: d.Time, y: (d.P50) / 1000000 }));

                datasets.push({
                    label: `${targetName} P50`,
                    data: p50Data,
                    borderColor: color,
                    backgroundColor: color,
                    fill: false,
                    tension: 0.1,
                    borderWidth: 2,
                    yAxisID: 'y'
                });
            }
        } else {
            // Single target - full line chart
            const p0Data = data.map(d => ({ x: d.Time, y: (d.P0 || d.MinNS) / 1000000 }));
            const p50Data = data.map(d => ({ x: d.Time, y: (d.P50) / 1000000 }));
            const p100Data = data.map(d => ({ x: d.Time, y: (d.P100 || d.MaxNS) / 1000000 }));
            const timeoutPercentageData = data.map(d => {
                const total = d.ProbeCount + d.TimeoutCount;
                return { x: d.Time, y: total > 0 ? (d.TimeoutCount / total) * 100 : 0 };
            });

            datasets.push({
                label: 'Timeout %',
                data: timeoutPercentageData,
                type: 'bar',
                backgroundColor: 'rgba(255, 99, 132, 0.5)',
                borderColor: 'rgba(255, 99, 132, 1)',
                borderWidth: 1,
                yAxisID: 'y1',
                barPercentage: 0.5
            });
            datasets.push({ label: 'Max (P100)', data: p100Data, borderColor: '#FF0000', backgroundColor: '#FF0000', fill: false, tension: 0.1, borderWidth: 1, yAxisID: 'y' });
            datasets.push({ label: 'Median (P50)', data: p50Data, borderColor: '#00FF00', backgroundColor: '#00FF00', fill: false, tension: 0.1, borderWidth: 2, yAxisID: 'y' });
            datasets.push({ label: 'Min (P0)', data: p0Data, borderColor: '#4B0082', backgroundColor: '#4B0082', fill: false, tension: 0.1, borderWidth: 1, yAxisID: 'y' });

            if (rawData && rawData.length > 0) {
                const scatterData = rawData.map(d => ({ x: d.Time, y: d.MinNS / 1000000 }));
                datasets.push({
                    label: 'Raw Latency (ms)',
                    data: scatterData,
                    type: 'scatter',
                    backgroundColor: 'black',
                    borderColor: 'black',
                    borderWidth: 2,
                    pointRadius: 2,
                    yAxisID: 'y'
                });
            }
        }

        const chartOptions = {
            responsive: true,
            maintainAspectRatio: false,
            interaction: { mode: 'index', intersect: false },
            scales: {
                x: {
                    type: 'time',
                    time: { displayFormats: TIME_DISPLAY_FORMATS },
                    grid: { display: false },
                    ticks: { maxRotation: 45, minRotation: 0 }
                },
                y: {
                    type: 'linear',
                    display: true,
                    position: 'left',
                    beginAtZero: true,
                    title: { display: true, text: 'Latency (ms)' }
                }
            },
            plugins: {
                zoom: {
                    pan: {
                        enabled: true,
                        mode: 'x',
                        onPanComplete: function ({ chart }) {
                            if (onZoomComplete) {
                                const { min, max } = chart.scales.x;
                                onZoomComplete(new Date(min), new Date(max));
                            }
                        }
                    },
                    zoom: {
                        wheel: {
                            enabled: true,
                            speed: 0.1
                        },
                        drag: {
                            enabled: true,
                            modifierKey: 'shift'
                        },
                        mode: 'x',
                        onZoomComplete: function ({ chart }) {
                            // Debounce zoom callback to avoid rapid reloads during wheel zoom
                            clearTimeout(zoomDebounceTimer);
                            zoomDebounceTimer = setTimeout(function () {
                                if (onZoomComplete) {
                                    const { min, max } = chart.scales.x;
                                    onZoomComplete(new Date(min), new Date(max));
                                }
                            }, 1000);
                        }
                    }
                },
                legend: { position: 'bottom' }
            }
        };

        // Add Y1 axis for timeout percentage (single target only)
        if (!multiTarget) {
            chartOptions.scales.y1 = {
                type: 'linear',
                display: true,
                position: 'right',
                beginAtZero: true,
                min: 0,
                max: 100,
                grid: { drawOnChartArea: false }
            };
        }

        return new Chart(chartCtx, {
            type: 'line',
            data: { datasets },
            options: chartOptions
        });
    }

    // ============================================
    // PUBLIC API
    // ============================================

    return {
        toLocalISO,
        TIME_DISPLAY_FORMATS,
        calculateSuggestedYMax,
        transformToBarData,
        createTimeoutBackgroundPlugin,
        createMedianLinePlugin,
        createHoverHighlightPlugin,
        createHeatmapGradient,
        createExternalTooltipHandler,
        renderLatencyChart
    };
})();
