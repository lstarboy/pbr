package material

import (
	"math"
	"math/rand"

	"github.com/hunterloftis/pbr/geom"
	"github.com/hunterloftis/pbr/rgb"
)

// Material describes the properties of a physically-based material
// Zero-value is a black, opaque, diffuse, non-metallic surface
type Material struct {
	d            MaterialDesc
	absorbance   rgb.Energy // Initd absorbance
	refract      float64    // Initd index of refraction
	fresnel      float64    // Initd average Fresnel value
	transmission rgb.Energy // Initd "alpha" value
}

// TODO: rename Gloss => Diffuse, add Gloss (top layer reflection)
// TODO: http://saarela.github.io/ShapeToolbox/gs-material-texture.html
// http://igorsklyar.com/system/images/development_descriptions/189/disney_1.jpeg?1432292046
type MaterialDesc struct {
	Color    rgb.Energy // Diffuse color for opaque surfaces, transmission coefficients for transparent surfaces
	Fresnel  rgb.Energy // Fresnel coefficients, used for fresnel reflectivity and computing the refractive index
	Light    rgb.Energy // Light emittance, used if this Material is a light source
	Transmit float64    // 0 = opaque, 1 = transparent, (0-1) = tinted thin surface
	Rough    float64    // Microfacet roughness (Material "polish")
	Metal    float64    // The metallic range of electric (1) or dielectric (0), controls energy absorption
	Thin     bool       // The material is a thin, double-sided surface
	Coat     float64    // Glossy clear-coat
}

func New(d MaterialDesc) *Material {
	m := Material{d: d}
	m.fresnel = math.Max(geom.Vector3(d.Fresnel).Ave(), 0.02)
	if d.Thin {
		m.transmission = d.Color.Amplified(d.Transmit)
		m.absorbance = rgb.Energy{0, 0, 0} // TODO: This is confusingly named (has nothing to do with m.absorb())
		m.refract = 1
	} else {
		m.transmission = rgb.Energy{d.Transmit, d.Transmit, d.Transmit}
		m.absorbance = rgb.Energy{
			X: 2 - math.Log10(d.Color.X*100),
			Y: 2 - math.Log10(d.Color.Y*100),
			Z: 2 - math.Log10(d.Color.Z*100),
		}
		// specular = ((ior - 1) / (ior +1))^2/0.08 <-- looks like would yield .fresnel * 0.1
		// (https://docs.blender.org/manual/en/dev/render/cycles/nodes/types/shaders/principled.html)
		m.refract = (1 + math.Sqrt(m.fresnel)) / (1 - math.Sqrt(m.fresnel))
	}
	return &m
}

// Bsdf is an attempt at a new bsdf
// TODO: a real BSDF instead of this procedural one.
// at each hit: choose between transmission, absorption, specular, or diffuse & generate next ray
// then pass incident & resulting directions into the bsdf to find the signal strength
// https://docs.blender.org/manual/en/dev/render/cycles/nodes/types/shaders/principled.html
// https://github.com/wdas/brdf/blob/master/src/brdfs/disney.brdf#L131
func (m *Material) Bsdf(norm, inc geom.Direction, dist float64, rnd *rand.Rand) (geom.Direction, rgb.Energy, bool) {
	if inc.Enters(norm) {
		// clear coat
		if rnd.Float64() < m.d.Coat {
			if rnd.Float64() < schlick(norm, inc, 0.04, 0, 0) {
				return m.shine(norm, inc, rnd)
			}
		}
		reflect := schlick(norm, inc, m.fresnel, 0, 0)
		switch {
		// reflect
		case rnd.Float64() < reflect:
			return m.reflect(norm, inc, rnd)
		// transmit (in)
		case rnd.Float64() < m.d.Transmit:
			return m.transmit(norm, inc, rnd)
		// absorb
		case rnd.Float64() < m.d.Metal: // TODO: is this extraneous? Should m.d.Metal just be pre-applied to m.d.Color?
			return m.absorb(inc)
		// diffuse
		default:
			return m.diffuse(norm, inc, rnd)
		}
	}
	if m.d.Thin {
		return m.Bsdf(norm.Inv(), inc, dist, rnd)
	}
	// transmit (out)
	return m.exit(norm, inc, dist, rnd)
}

// Emit returns the amount of light emitted from the Material at a given angle.
func (m *Material) Emit() rgb.Energy {
	return m.d.Light
}

func (m *Material) Diffuse() float64 {
	return m.d.Rough
}

func (m *Material) Color() rgb.Energy {
	return m.d.Color
}

// TODO: integrate with reflect?
func (m *Material) shine(norm, inc geom.Direction, rnd *rand.Rand) (geom.Direction, rgb.Energy, bool) {
	// TODO: if reflection enters the normal, invert the reflection about the normal
	if refl := inc.Reflected(norm); !refl.Enters(norm) {
		return refl, m.d.Color, false
	}
	return m.diffuse(norm, inc, rnd)
}

func (m *Material) reflect(norm, inc geom.Direction, rnd *rand.Rand) (geom.Direction, rgb.Energy, bool) {
	// TODO: if reflection enters the normal, invert the reflection about the normal
	if refl := inc.Reflected(norm).Cone(m.d.Rough, rnd); !refl.Enters(norm) {
		return refl, rgb.Energy(geom.UnitVector3.Lerp(geom.Vector3(m.d.Fresnel), m.d.Metal)), false
	}
	return m.diffuse(norm, inc, rnd)
}

func (m *Material) transmit(norm, inc geom.Direction, rnd *rand.Rand) (geom.Direction, rgb.Energy, bool) {
	if entered, refr := inc.Refracted(norm, 1, m.refract); entered {
		if spread := refr.Cone(m.d.Rough, rnd); spread.Enters(norm) {
			return spread, m.transmission, false
		}
		return refr, m.transmission, false
	}
	return m.diffuse(norm, inc, rnd)
}

func (m *Material) exit(norm, inc geom.Direction, dist float64, rnd *rand.Rand) (geom.Direction, rgb.Energy, bool) {
	if m.d.Transmit == 0 {
		// shallow bounce within margin of error
		// isn't really an intersection, so just keep the ray moving
		return inc, rgb.Full, false
	}
	if rnd.Float64() >= schlick(norm, inc, 0, m.refract, 1.0) {
		if exited, refr := inc.Refracted(norm.Inv(), m.refract, 1); exited {
			if spread := refr.Cone(m.d.Rough, rnd); !spread.Enters(norm) {
				return spread, beers(dist, m.absorbance), false
			}
			return refr, beers(dist, m.absorbance), false
		}
	}
	return inc.Reflected(norm.Inv()), beers(dist, m.absorbance), false
}

func (m *Material) diffuse(norm, inc geom.Direction, rnd *rand.Rand) (geom.Direction, rgb.Energy, bool) {
	return norm.RandHemiCos(rnd), m.d.Color.Amplified(1 / math.Pi), true
}

func (m *Material) absorb(inc geom.Direction) (geom.Direction, rgb.Energy, bool) {
	return inc, rgb.Energy{}, false
}
