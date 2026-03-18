export interface EffectSettings {
  fx: number;
  fy: number;
  scale: number;
}

export class WebGlEffectRenderer {
  readonly canvas: HTMLCanvasElement;
  readonly available: boolean;
  private readonly gl: WebGLRenderingContext | null;
  private readonly program: WebGLProgram | null;
  private readonly positionLocation: number;
  private readonly samplerLocation: WebGLUniformLocation | null;
  private readonly lensSLocation: WebGLUniformLocation | null;
  private readonly lensFLocation: WebGLUniformLocation | null;
  private readonly texture: WebGLTexture | null;
  private readonly vertexBuffer: WebGLBuffer | null;

  constructor(canvas: HTMLCanvasElement) {
    this.canvas = canvas;
    this.gl = this.canvas.getContext('webgl', {
      alpha: false,
      antialias: false,
      depth: false,
      stencil: false,
      preserveDrawingBuffer: false,
    });
    this.available = false;
    this.program = null;
    this.positionLocation = -1;
    this.samplerLocation = null;
    this.lensSLocation = null;
    this.lensFLocation = null;
    this.texture = null;
    this.vertexBuffer = null;
    if (!this.gl) {
      return;
    }

    const vertexSrc = `
      attribute vec3 aVertexPosition;
      varying vec3 vPosition;
      void main(void) {
        vPosition = aVertexPosition;
        gl_Position = vec4(vPosition, 1.0);
      }
    `;

    const fragmentSrc = `
      precision highp float;
      uniform vec3 uLensS;
      uniform vec2 uLensF;
      uniform sampler2D uSampler;
      varying vec3 vPosition;
      vec2 glCoordToTextureCoord(vec2 glCoord) {
        return glCoord * vec2(1.0, -1.0) / 2.0 + vec2(0.5, 0.5);
      }
      void main(void) {
        float scale = uLensS.z;
        vec3 vPos = vPosition;
        float fx = uLensF.x;
        float fy = uLensF.y;
        vec2 mapping = vPos.xy;
        mapping.x = mapping.x + ((pow(vPos.y, 2.0) / scale) * vPos.x / scale) * -fx;
        mapping.y = mapping.y + ((pow(vPos.x, 2.0) / scale) * vPos.y / scale) * -fy;
        mapping = mapping * uLensS.xy;
        mapping = glCoordToTextureCoord(mapping / scale);
        vec4 color = texture2D(uSampler, mapping);
        if (mapping.x > 0.99 || mapping.x < 0.01 || mapping.y > 0.99 || mapping.y < 0.01) {
          color = vec4(0.0, 0.0, 0.0, 1.0);
        }
        gl_FragColor = color;
      }
    `;

    const program = this.createProgram(vertexSrc, fragmentSrc);
    this.program = program;
    this.positionLocation = this.gl.getAttribLocation(program, 'aVertexPosition');
    this.samplerLocation = this.gl.getUniformLocation(program, 'uSampler');
    this.lensSLocation = this.gl.getUniformLocation(program, 'uLensS');
    this.lensFLocation = this.gl.getUniformLocation(program, 'uLensF');
    this.texture = this.gl.createTexture();
    const vertices = new Float32Array([
      -1.0, -1.0, 0.0,
      1.0, -1.0, 0.0,
      1.0, 1.0, 0.0,
      -1.0, -1.0, 0.0,
      1.0, 1.0, 0.0,
      -1.0, 1.0, 0.0,
    ]);
    this.vertexBuffer = this.gl.createBuffer();
    this.gl.bindBuffer(this.gl.ARRAY_BUFFER, this.vertexBuffer);
    this.gl.bufferData(this.gl.ARRAY_BUFFER, vertices, this.gl.STATIC_DRAW);
    this.gl.bindTexture(this.gl.TEXTURE_2D, this.texture);
    this.gl.texParameteri(this.gl.TEXTURE_2D, this.gl.TEXTURE_WRAP_S, this.gl.CLAMP_TO_EDGE);
    this.gl.texParameteri(this.gl.TEXTURE_2D, this.gl.TEXTURE_WRAP_T, this.gl.CLAMP_TO_EDGE);
    this.gl.texParameteri(this.gl.TEXTURE_2D, this.gl.TEXTURE_MIN_FILTER, this.gl.LINEAR);
    this.gl.texParameteri(this.gl.TEXTURE_2D, this.gl.TEXTURE_MAG_FILTER, this.gl.LINEAR);
    this.gl.pixelStorei(this.gl.UNPACK_FLIP_Y_WEBGL, false);
    this.gl.useProgram(program);
    this.gl.uniform1i(this.samplerLocation, 0);
    this.gl.clearColor(0, 0, 0, 1);
    (this as { available: boolean }).available = true;
  }

  private createShader(type: number, source: string): WebGLShader {
    if (!this.gl) {
      throw new Error('WebGL unavailable');
    }
    const shader = this.gl.createShader(type);
    if (!shader) {
      throw new Error('Failed to create shader');
    }
    this.gl.shaderSource(shader, source);
    this.gl.compileShader(shader);
    if (!this.gl.getShaderParameter(shader, this.gl.COMPILE_STATUS)) {
      throw new Error(this.gl.getShaderInfoLog(shader) || 'shader compile failed');
    }
    return shader;
  }

  private createProgram(vertexSrc: string, fragmentSrc: string): WebGLProgram {
    if (!this.gl) {
      throw new Error('WebGL unavailable');
    }
    const vertexShader = this.createShader(this.gl.VERTEX_SHADER, vertexSrc);
    const fragmentShader = this.createShader(this.gl.FRAGMENT_SHADER, fragmentSrc);
    const program = this.gl.createProgram();
    if (!program) {
      throw new Error('Failed to create program');
    }
    this.gl.attachShader(program, vertexShader);
    this.gl.attachShader(program, fragmentShader);
    this.gl.linkProgram(program);
    if (!this.gl.getProgramParameter(program, this.gl.LINK_STATUS)) {
      throw new Error(this.gl.getProgramInfoLog(program) || 'program link failed');
    }
    return program;
  }

  resize(width: number, height: number): void {
    if (!this.gl) {
      return;
    }
    if (this.canvas.width === width && this.canvas.height === height) {
      return;
    }
    this.canvas.width = width;
    this.canvas.height = height;
    this.gl.viewport(0, 0, width, height);
  }

  draw(source: HTMLCanvasElement, settings: EffectSettings): boolean {
    if (!this.gl || !this.available || !this.program || !this.texture || !this.vertexBuffer) {
      return false;
    }
    const width = Math.max(1, source.width);
    const height = Math.max(1, source.height);
    this.resize(width, height);
    this.gl.useProgram(this.program);
    this.gl.activeTexture(this.gl.TEXTURE0);
    this.gl.bindTexture(this.gl.TEXTURE_2D, this.texture);
    this.gl.texImage2D(this.gl.TEXTURE_2D, 0, this.gl.RGBA, this.gl.RGBA, this.gl.UNSIGNED_BYTE, source);
    this.gl.bindBuffer(this.gl.ARRAY_BUFFER, this.vertexBuffer);
    this.gl.enableVertexAttribArray(this.positionLocation);
    this.gl.vertexAttribPointer(this.positionLocation, 3, this.gl.FLOAT, false, 0, 0);
    this.gl.uniform3fv(this.lensSLocation, new Float32Array([1, 1, settings.scale]));
    this.gl.uniform2fv(this.lensFLocation, new Float32Array([settings.fx, settings.fy]));
    this.gl.clear(this.gl.COLOR_BUFFER_BIT);
    this.gl.drawArrays(this.gl.TRIANGLES, 0, 6);
    return true;
  }
}
